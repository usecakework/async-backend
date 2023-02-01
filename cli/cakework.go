package main

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"github.com/urfave/cli/v2"
	urfaveCli "github.com/urfave/cli/v2"
	"github.com/usecakework/cakework/lib/auth"
	cwConfig "github.com/usecakework/cakework/lib/config"
	flyUtil "github.com/usecakework/cakework/lib/fly"
	flyCli "github.com/usecakework/cakework/lib/fly/cli"
	"github.com/usecakework/cakework/lib/frontendclient"
	cwHttp "github.com/usecakework/cakework/lib/http"
	"github.com/usecakework/cakework/lib/shell"
	"github.com/usecakework/cakework/lib/types"
)

// TODO put stuff into templates for different languages
//go:embed fly.toml
var flyConfig embed.FS

//go:embed .env
var envFile []byte

//go:embed newprojfiles/Makefile
var makefile embed.FS

//go:embed newprojfiles/assets
var assets embed.FS

var config cwConfig.Config
var configFile string
var credsProvider auth.BearerCredentialsProvider

var frontendClient *frontendclient.Client
var FRONTEND_URL string

func main() {

	var appName string
	var language string
	var appDirectory string
	var headless bool = false

	workingDirectory, _ := os.Getwd()
	buildDirectory := workingDirectory
	dirname, _ := os.UserHomeDir()
	cakeworkDirectory := dirname + "/.cakework"

	viper.SetConfigType("dotenv")
	err := viper.ReadConfig(bytes.NewBuffer(envFile))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	if err != nil {
		fmt.Println(fmt.Errorf("%w", err))
		os.Exit(1)
	}

	log.Debug("appName: " + appName)
	log.Debug("language: " + language)
	log.Debug("appDirectory: " + appDirectory)
	log.Debug("buildDirectory: " + buildDirectory)

	configFile = filepath.Join(cakeworkDirectory, "config.json")
	config, err := cwConfig.LoadConfig(configFile)
	if err != nil {
		fmt.Println("Could not load config file.")
		fmt.Println(err)
		os.Exit(1)
	}

	config.FilePath = configFile
	cwConfig.UpdateConfig(*config, configFile)

	credsProvider = auth.BearerCredentialsProvider{ConfigFile: configFile}

	FRONTEND_URL = viper.GetString("FRONTEND_URL")

	frontendClient = frontendclient.New(FRONTEND_URL, credsProvider)

	app := &urfaveCli.App{
		Name:     "cakework",
		Usage:    "This is the Cakework command line interface",
		Version:  "v1.1.0", // TODO automatically update this and tie this to the goreleaser version
		Compiled: time.Now(),
		Flags: []urfaveCli.Flag{
			&urfaveCli.BoolFlag{Name: "verbose", Hidden: true},
		},
		Authors: []*urfaveCli.Author{
			{
				Name:  "Jessie Young",
				Email: "jessie@cakework.com",
			},
		},
		Before: func(cCtx *urfaveCli.Context) error {
			if !cCtx.Bool("verbose") {
				log.SetLevel(log.ErrorLevel) // default behavior (not verbose) is to not log anything.
			} else {
				log.SetLevel(log.DebugLevel)
			}
			return nil
		},
		Commands: []*urfaveCli.Command{
			{ // if don't get the result within x seconds, kill
				Name:  "login", // TODO change this to signup. // TODO also create a logout
				Usage: "Authenticate the Cakework CLI",
				Flags: []urfaveCli.Flag{
					&urfaveCli.BoolFlag{Name: "headless", Destination: &headless},
				},
				Action: func(cCtx *cli.Context) error {
					if isLoggedIn(*config) {
						fmt.Println("You are already logged in 🍰")
						return nil
					}

					// when we auth (sign up or log in) for the first time, obtain a set of tokens
					err = signUpOrLogin(headless)
					if err != nil {
						return fmt.Errorf("Error signing up / logging in: %w", err)
					}

					fmt.Println("You are logged in 🍰")
					return nil
				},
			},
			{
				Name:  "signup", // TODO change this to signup. // TODO also create a logout
				Usage: "Sign up for Cakework",
				Flags: []urfaveCli.Flag{
					&urfaveCli.BoolFlag{Name: "headless", Destination: &headless},
				},
				Action: func(cCtx *cli.Context) error {
					if isLoggedIn(*config) {
						fmt.Println("You are already logged in 🍰")
						return nil
					}
					err := signUpOrLogin(headless)
					if err != nil {
						return fmt.Errorf("Error signing up: %w", err)
					}

					userId, err := getUserId(configFile)
					if err != nil {
						return fmt.Errorf("Error signing up: %w", err)
					}

					frontendClient := frontendclient.New(FRONTEND_URL, credsProvider)
					user, err := frontendClient.GetUser(userId)
					if err != nil {
						return fmt.Errorf("Error signing up: %w", err)
					}

					if user != nil {
						fmt.Println("You're logged in with your existing account.")
						return nil
					}

					user, err = frontendClient.CreateUser(userId)
					if err != nil {
						return fmt.Errorf("Error signing up : %w", err)
					}

					fmt.Println("You're signed up! 🍰")
					return nil
				},
			},
			{
				Name:  "logout",
				Usage: "Log out of the Cakework CLI",
				Action: func(cCtx *cli.Context) error {
					err := os.Remove(configFile)
					if err != nil {
						return err
					}

					fmt.Println("You have been logged out")
					return nil
				},
			},
			{
				Name:      "create-client-token", // TODO change this to signup. // TODO also create a logout
				Usage:     "Create an access token for your clients",
				UsageText: "cakework create-client-token [TOKEN_NAME] [command options] [arguments...]",
				Action: func(cCtx *cli.Context) error {
					if !isLoggedIn(*config) {
						fmt.Println("Please signup (cakework signup) or log in (cakework login).")
						return nil
					}

					var name string
					if cCtx.NArg() > 0 {
						name = cCtx.Args().Get(0)
						// write out app name to config file
						// TODO in the future we won't
						// TODO write this out in json form

					} else {
						return errors.New("Please specify a name for the client token.")
					}

					userId, err := getUserId(configFile)
					if err != nil {
						return fmt.Errorf("Error getting user details to create a client token with: %w", err)
					}
					frontendClient := frontendclient.New(FRONTEND_URL, credsProvider)
					clientToken, err := frontendClient.CreateClientToken(userId, name)
					if err != nil {
						return fmt.Errorf("Error creating a client token: %w", err)
					}

					fmt.Println("Created client token:")
					fmt.Println(clientToken.Token)
					fmt.Println()
					fmt.Println("Store this token securely. You will not be able to see this again.")

					return nil
				},
			},
			{
				Name:      "new",
				Usage:     "Create a new project",
				UsageText: "cakework new [flags] [PROJECT_NAME]",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:        "lang",
						Value:       "python",
						Usage:       "language for the project. Defaults to python 3.8",
						Destination: &language,
					},
				},
				Action: func(cCtx *cli.Context) error {
					if !isLoggedIn(*config) {
						fmt.Println("Please signup (cakework signup) or log in (cakework login).")
						return nil
					}

					if cCtx.NArg() > 0 {
						appName = cCtx.Args().Get(0)
					} else {
						return errors.New("Please include the name of your new project")
					}

					lang := cCtx.String("lang")
					if lang != "python" {
						return errors.New("Language " + lang + " not supported.")
					}

					fmt.Println("Creating your new Cakework project " + appName + "!")
					fmt.Println("")

					// TODO make a separate cakework specific build directory and then copy everything out

					rootDir := "newprojfiles"

					// copy Makefile into current build directory
					text, err := makefile.ReadFile(rootDir + "/Makefile")
					if err != nil {
						return fmt.Errorf("Error getting required build assets: %w", err)
					}
					err = os.WriteFile(filepath.Join(appDirectory+"Makefile"), text, 6044)
					if err != nil {
						return fmt.Errorf("Error getting required build assets: %w", err)
					}

					assetsDir := rootDir + "/assets"
					// copy all assets into current build directory
					buildAssets, err := fs.ReadDir(assets, assetsDir)
					if err != nil {
						return fmt.Errorf("Error getting required build assets: %w", err)
					}

					err = os.Mkdir("assets", os.ModePerm)
					if err != nil {
						return fmt.Errorf("Error getting required build assets: %w", err)
					}

					for _, asset := range buildAssets {

						text, err := assets.ReadFile(assetsDir + "/" + asset.Name())
						if err != nil {
							return fmt.Errorf("Error getting required build assets: %w", err)
						}

						var name string = asset.Name()

						// golang doesn't look for hidden files
						if name[0:3] == "dot" {
							name = "." + name[3:]
						}

						err = os.WriteFile(filepath.Join("assets", name), text, 6044)
						if err != nil {
							return fmt.Errorf("Error getting required build assets: %w", err)
						}
					}

					// clean up files no matter what
					defer func() {
						cleanCommand := "make clean"
						cmd := exec.Command("bash", "-c", cleanCommand)
						err = shell.RunCmdLive(cmd)
						if err != nil {
							fmt.Println("Error cleaning up temp assets")
							os.Exit(1)
						}
					}()

					// generate client token
					userId, err := getUserId(configFile)
					if err != nil {
						return fmt.Errorf("Error getting user details to create a client token with: %w", err)
					}
					frontendClient := frontendclient.New(FRONTEND_URL, credsProvider)
					clientToken, err := frontendClient.CreateClientToken(userId, appName)
					if err != nil {
						return fmt.Errorf("Error creating a client token: %w", err)
					}

					if clientToken == nil {
						fmt.Println("Failed to create a client token")
						os.Exit(1)
					}

					// run makefile
					makeCommand := "CAKEWORK_APP_NAME=" + appName + " CAKEWORK_CLIENT_TOKEN=" + clientToken.Token + " make new"
					cmd := exec.Command("bash", "-c", makeCommand)
					err = shell.RunCmdLive(cmd)
					if err != nil {
						return fmt.Errorf("Error creating a new project: %w", err)
					}

					// s.Stop()

					return nil
				},
			},
			{
				Name:  "deploy",
				Usage: "Deploy your Project",
				Action: func(cCtx *urfaveCli.Context) error {
					var secrets *types.CLISecrets
					var err error
					secrets, err = frontendClient.GetCLISecrets()
					if err != nil {
						fmt.Println("Failed to get FLY access token from Cakework frontend")
						fmt.Println(err)
						os.Exit(1)
					}

					log.Debug("got secrets")
					log.Debug(secrets)

					FLY_ACCESS_TOKEN := secrets.FLY_ACCESS_TOKEN
					if FLY_ACCESS_TOKEN == "" {
						fmt.Println("Fly access token from frontend service is null")
						os.Exit(1)
					}

					FLY_ORG := viper.GetString("FLY_ORG")
					fly := flyCli.New(dirname+"/.cakework/.fly/bin/fly", FLY_ACCESS_TOKEN, FLY_ORG)

					if !isLoggedIn(*config) {
						fmt.Println("Please signup (cakework signup) or log in (cakework login).")
						return nil
					}

					// TODO this won't work if they change the folder name
					srcDir := workingDirectory + "/" + strings.ReplaceAll(strings.ToLower(filepath.Base(workingDirectory)), "-", "_")

					fmt.Println("Deploying Your Project...")
					readFile, err := os.Open(filepath.Join(srcDir, "main.py"))
					if err != nil {
						fmt.Println(err)
						return fmt.Errorf("There was an error deploying your project. Please make sure you're in the project directory")
					}

					fileScanner := bufio.NewScanner(readFile)
					fileScanner.Split(bufio.ScanLines)

					var rgxAppName = regexp.MustCompile(`\(\"([^)]+)\"\)`)
					var projectName string

					var rgxTaskName = regexp.MustCompile(`\(([^)]+)\)`)
					var taskName string

					defer readFile.Close()

					// TODO this is janky. can now get app name from config; how to make this less janky for getting the registered activity name?
					for fileScanner.Scan() {
						line := fileScanner.Text()
						if strings.Contains(line, "Cakework(") {
							rs := rgxAppName.FindAllStringSubmatch(line, -1)
							for _, i := range rs {
								projectName = i[1]
							}
						}
						if strings.Contains(line, "add_task") {
							rs := rgxTaskName.FindAllStringSubmatch(line, -1)
							for _, i := range rs {
								taskName = i[1]
							}
						}
					}

					if projectName == "" {
						return errors.New("Failed to parse project name from main.py. Please make sure you're in the project directory!")
					}
					if taskName == "" {
						return cli.Exit("Failed to parse task name from main.py. Please make sure you're in the project directory!", 1)
					}

					userId, err := getUserId(configFile)
					if err != nil {
						return fmt.Errorf("Failed to get user from cakework config: %w", err)
					}

					flyAppName := flyUtil.GetFlyAppName(userId, projectName, taskName)

					s := spinner.New(spinner.CharSets[9], 100*time.Millisecond) // Build our new spinner
					s.Start()                                                   // Start the spinner

					// TODO: instead of just deleting and re-creating the app, we can just delete the old machines
					// TODO figure out how to deploy fly machine instead of fly app
					// if name is already taken, want to make sure we don't overwrite it; need to make use of the old fly.toml. Should we store that for the user?

					// copy fly.toml
					text, _ := flyConfig.ReadFile("fly.toml")
					os.WriteFile(filepath.Join(buildDirectory, "fly.toml"), text, 0644)

					// update the fly.toml file
					flyConfig := filepath.Join(buildDirectory, "fly.toml")
					input, err := os.ReadFile(flyConfig)

					lines := strings.Split(string(input), "\n")

					// note: this is brittle (what if they don't have app with space?)
					for i, line := range lines {
						if strings.Contains(line, "app =") {
							lines[i] = "app = \"" + flyAppName + "\""
						}
					}
					output := strings.Join(lines, "\n")
					err = ioutil.WriteFile(flyConfig, []byte(output), 0644)
					if err != nil {
						return fmt.Errorf("Failed to write fly config. %w", err)
					}

					// TODO remove access token from source code and re-create github repo

					// TODO move these parameters to env variables
					if out, err := fly.CreateApp(flyAppName, buildDirectory); err != nil {
						return errors.New("Failed to create Fly app\n" + out)
					}

					// TODO if ips have previously been allocated, skip this step
					if _, err := fly.AllocateIpv4(flyAppName, buildDirectory); err != nil {
						return errors.New("Failed to allocate ips for Fly app")
					}

					// otherwise, create new machine
					// TODO if new machine tried to start and was stopped because the code had an errr, don't wait until timeout (60 seconds)
					// TODO fail the command if the machine was stopped
					// TODO debug why not able to set restart policy. still seeing "machine did not have a restart policy, defaulting to restart" shows success
					out, err := fly.NewMachine(flyAppName, buildDirectory)
					if err != nil {
						return errors.New("Failed to deploy app to Fly machine")
					}

					machineId, state, image, err := fly.GetMachineInfo(out)

					if err != nil {
						return errors.New("Failed to get info from fly machine")
					}

					log.Debugf("machineId: %s state: %s image: %s", machineId, state, image)

					// make this a shared variable?
					// how to make sure the tokens are up to date?
					// frontendClient := frontendclient.New("https://cakework-frontend.fly.dev", config.AccessToken, config.RefreshToken, "")
					// frontendClient := frontendclient.New("https://cakework-frontend.fly.dev", credsProvider)
					frontendClient := frontendclient.New(FRONTEND_URL, credsProvider)

					name := uuid.New().String() // generate a random string for the name
					err = frontendClient.CreateMachine(userId, projectName, taskName, name, machineId, state, image, "CLI")
					if err != nil {
						return errors.New("Failed to store deployed task in database\n" + fmt.Sprint(err))
					}

					s.Stop()

					// TODO run thcis (even if file doesn't exist) after every
					err = os.Remove(filepath.Join(buildDirectory, "fly.toml"))
					if err != nil {
						return errors.New("Failed to clean up build artifacts")
					}

					// TODO: make sure that the machine passes health checks (the grpc server with the task started successfully)
					fmt.Println("Successfully deployed your tasks! 🍰")
					return nil
				},
			},
			{
				Name:  "task",
				Usage: "Interact with your Tasks (e.g. get logs)",
				Subcommands: []*cli.Command{
					{
						Name:      "logs",
						Usage:     "Get request logs for a task",
						UsageText: "cakework task logs [flags] [PROJECT_NAME] [TASK_NAME]",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:  "status",
								Usage: "Status to filter by. PENDING, IN_PROGRESS, SUCCEEDED, or FAILED",
							},
						},
						Action: func(cCtx *cli.Context) error {
							if !isLoggedIn(*config) {
								fmt.Println("Please signup (cakework signup) or log in (cakework login).")
								return nil
							}

							if cCtx.NArg() < 2 {
								return errors.New("Please specify Project name and Task name.")
							}

							appName := cCtx.Args().Get(0)
							taskName := cCtx.Args().Get(1)

							statusFilter := cCtx.String("status")

							userId, err := getUserId(configFile)
							if err != nil {
								return fmt.Errorf("Could not get user for getting logs: %w", err)
							}

							frontendClient := frontendclient.New(FRONTEND_URL, credsProvider)

							taskLogs, err := frontendClient.GetTaskLogs(userId, appName, taskName, statusFilter)
							if err != nil {
								return fmt.Errorf("Could not get task logs: %w", err)
							}

							if len(taskLogs.Runs) == 0 {
								fmt.Println("No runs found. Check your Project name and Task name!")
								return nil
							}

							t := table.NewWriter()
							t.SetOutputMirror(os.Stdout)
							t.AppendHeader(table.Row{"Run Id", "Status", "Started", "Updated", "Parameters", "Result"})
							for _, request := range taskLogs.Runs {
								t.AppendRow([]interface{}{
									request.RunId,
									request.Status,
									time.Unix(request.CreatedAt, 0).Format("02 Jan 06 15:04 MST"),
									time.Unix(request.UpdatedAt, 0).Format("02 Jan 06 15:04 MST"),
									request.Parameters,
									request.Result,
								})
							}
							t.Render()

							return nil
						},
					},
				},
			},
			{
				Name:  "run",
				Usage: "Interact with your Runs (e.g. get logs)",
				Subcommands: []*cli.Command{
					{
						Name:      "status",
						Usage:     "Get processing status for a Run",
						UsageText: "cakework run status [RUN_ID]",
						Action: func(cCtx *cli.Context) error {
							if !isLoggedIn(*config) {
								fmt.Println("Please signup (cakework signup) or log in (cakework login).")
								return nil
							}

							if cCtx.NArg() != 1 {
								return errors.New("Please include one parameter, the Run ID")
							}
							runId := cCtx.Args().Get(0)

							userId, err := getUserId(configFile)
							if err != nil {
								return fmt.Errorf("Error getting user from config. %w", err)
							}

							frontendClient := frontendclient.New(FRONTEND_URL, credsProvider)
							requestStatus, err := frontendClient.GetRunRequestStatus(userId, runId)
							if err != nil {
								return fmt.Errorf("Error getting request status from server. %w", err)
							}

							if requestStatus == "" {
								fmt.Println("Request not found. Please check your Run Id.")
								return nil
							}

							fmt.Println(requestStatus)
							return nil
						},
					},
					{
						Name:      "logs",
						Usage:     "Get logs for a Run",
						UsageText: "cakework run logs [RUN_ID]",
						Action: func(cCtx *cli.Context) error {
							if !isLoggedIn(*config) {
								fmt.Println("Please signup (cakework signup) or log in (cakework login).")
								return nil
							}

							if cCtx.NArg() != 1 {
								return errors.New("Please include one parameter, the Run ID")
							}
							runId := cCtx.Args().Get(0)

							userId, err := getUserId(configFile)
							if err != nil {
								return fmt.Errorf("Error getting user from config. %w", err)
							}

							s := spinner.New(spinner.CharSets[9], 100*time.Millisecond)
							s.Start()

							frontendClient := frontendclient.New(FRONTEND_URL, credsProvider)
							requestLogs, err := frontendClient.GetRunLogs(userId, runId)
							s.Stop()
							if err != nil {
								return fmt.Errorf("Error getting request logs %w", err)
							}

							if requestLogs == nil {
								fmt.Println("Request not found. Please check your Run Id.")
								return nil
							}

							if len(requestLogs.LogLines) == 0 {
								fmt.Println("No logs found for this run.")
								return nil
							}

							for _, line := range requestLogs.LogLines {
								timestampInt, err := strconv.ParseInt(line.Timestamp, 10, 64)
								if err != nil {
									return errors.New("Error printing logs")
								}

								timestamp := time.UnixMilli(timestampInt).Format("02-Jan-06T15:04:05.00")
								fmt.Println(timestamp + "  " + line.LogLevel + "  " + line.Message)
							}

							return nil
						},
					},
				},
			},
		},
	}

	err = app.Run(os.Args)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

// File copies a single file from src to dst
func File(src, dst string) error {
	var err error
	var srcfd *os.File
	var dstfd *os.File
	var srcinfo os.FileInfo

	if srcfd, err = os.Open(src); err != nil {
		return err
	}
	defer srcfd.Close()

	if dstfd, err = os.Create(dst); err != nil {
		return err
	}
	defer dstfd.Close()

	if _, err = io.Copy(dstfd, srcfd); err != nil {
		return err
	}
	if srcinfo, err = os.Stat(src); err != nil {
		return err
	}
	return os.Chmod(dst, srcinfo.Mode())
}

// Dir copies a whole directory recursively
func Dir(src string, dst string) error {
	var err error
	var fds []os.FileInfo
	var srcinfo os.FileInfo

	if srcinfo, err = os.Stat(src); err != nil {
		return err
	}

	if err = os.MkdirAll(dst, srcinfo.Mode()); err != nil {
		return err
	}

	if fds, err = ioutil.ReadDir(src); err != nil {
		return err
	}
	for _, fd := range fds {
		srcfp := path.Join(src, fd.Name())
		dstfp := path.Join(dst, fd.Name())

		if fd.IsDir() {
			if err = Dir(srcfp, dstfp); err != nil {
				log.Error(err)
			}
		} else {
			if err = File(srcfp, dstfp); err != nil {
				log.Error(err)
			}
		}
	}
	return nil
}

func openBrowser(url string) error {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}

	return err
}

func signUpOrLogin(headless bool) error {
	log.Debug("Starting log in flow")
	var data map[string]interface{}

	// if exists already and a user is found, can skip the log in

	AUTH0_DEVICE_CODE_URL := viper.GetString("AUTH0_DEVICE_CODE_URL")

	// if using the creds to call an api, need to use the API's Identifier as the audience
	AUTH0_CLIENT_ID := viper.GetString("AUTH0_CLIENT_ID")
	FRONTEND_URL_AUTH0 := "https%3A%2F%2Fcakework-frontend.fly.dev" // viper.GetString("FRONTEND_URL_AUTH0")

	payload := strings.NewReader("client_id=" + AUTH0_CLIENT_ID + "&scope=openid offline_access external %7D&audience=" + FRONTEND_URL_AUTH0)
	req, _ := http.NewRequest("POST", AUTH0_DEVICE_CODE_URL, payload)
	req.Header.Add("content-type", "application/x-www-form-urlencoded")

	res, err := cwHttp.CallHttpV2(req)

	if res.StatusCode != 200 {
		return errors.New("Failed to log in using device code " + res.Status)
	}

	err = json.NewDecoder(res.Body).Decode(&data)
	if err != nil {
		return err
	}
	verificationUrlNotComplete := data["verification_uri"].(string)
	verificationUrl := data["verification_uri_complete"].(string)
	deviceCode := data["device_code"].(string)
	userCode := data["user_code"].(string)

	fmt.Println("Your login code is: " + userCode)
	if headless {
		fmt.Println("Open the login website in a browser and enter your code:")
		fmt.Println(verificationUrlNotComplete)
	} else {
		err = openBrowser(verificationUrl)
		if err != nil {
			fmt.Println("Could not open browser. Run login/signup with --headless")
			return err
		}
	}

	s := spinner.New(spinner.CharSets[9], 100*time.Millisecond) // Build our new spinner
	s.Start()                                                   // Start the spinner

	var accessToken string
	var refreshToken string
	// poll for request token
	// Q: make it so that we only try for up to X minutes
	for {
		AUTH0_TOKEN_URL := viper.GetString("AUTH0_TOKEN_URL")
		AUTH0_CLIENT_ID := viper.GetString("AUTH0_CLIENT_ID")

		payload = strings.NewReader("grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code&device_code=" + deviceCode + "&client_id=" + AUTH0_CLIENT_ID)

		req, _ = http.NewRequest("POST", AUTH0_TOKEN_URL, payload)

		log.Debug("payload to /token endpoint:")
		req.Header.Add("content-type", "application/x-www-form-urlencoded")

		res, err = cwHttp.CallHttpV2(req)
		if err != nil {
			return err
		}

		err = json.NewDecoder(res.Body).Decode(&data)
		if err != nil {
			return err
		}
		if _, ok := data["access_token"]; ok {
			log.Debug("Successfully got an access token!")
			accessToken = data["access_token"].(string)
			refreshToken = data["refresh_token"].(string)
			break
		} else {
			time.Sleep(5 * time.Second) // TODO actually get the interval from above
		}
	}

	log.Debug("access_token: " + accessToken)
	log.Debug("refresh_token: " + refreshToken)

	config.AccessToken = accessToken
	config.RefreshToken = refreshToken
	cwConfig.UpdateConfig(config, configFile)

	// TODO: we should store the accessToken and refreshToken
	// call the /userInfo API to get the user information

	// technically don't need to make a call to this; can parse the jwt token to get the sub field.
	AUTH0_USERINFO_URL := viper.GetString("AUTH0_USERINFO_URL")

	req, _ = http.NewRequest("GET", AUTH0_USERINFO_URL, nil)

	res, err = cwHttp.CallHttpAuthedV2(req, credsProvider)
	if err != nil {
		return err
	}

	if res.StatusCode != 200 {
		log.Debug(res.StatusCode)
		log.Debug(res)
		log.Debug(data)
		return errors.New("Failed to get user info " + res.Status)
	}
	err = json.NewDecoder(res.Body).Decode(&data)
	if err != nil {
		return err
	}
	sub := data["sub"].(string)
	userId := strings.Split(sub, "|")[1]
	config.UserId = userId
	cwConfig.UpdateConfig(config, configFile)

	s.Stop()

	return nil
}

func isLoggedIn(config cwConfig.Config) bool {
	if config.UserId != "" {
		return true
	}
	return false
}

// // should only call if a user is logged in
func getUserId(configFile string) (string, error) {
	config, err := cwConfig.LoadConfig(configFile)
	if err != nil {
		return "", err
	}
	return config.UserId, nil // TODO may want to do some checks. assume this returns not ""
}

// this also writes to the config file
// note: field name needs to be in all caps!
func addConfigValue(field string, value string) error {
	v := reflect.ValueOf(&config).Elem().FieldByName(field)
	if v.IsValid() {
		v.SetString(value)
	}

	file, err := json.MarshalIndent(config, "", " ")
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(configFile, file, 0644)
	if err != nil {
		return err
	}

	return nil
}

type CustomClaimsExample struct {
	Scope string `json:"scope"`
}

// Validate errors out if `ShouldReject` is true.
func (c *CustomClaimsExample) Validate(ctx context.Context) error {
	// if c.ShouldReject {
	// 	return errors.New("should reject was set to true")
	// }
	return nil
}

func (c *CustomClaimsExample) Valid() error {
	return nil
}
