/*
    This Sahale app does some basic speech analytics. It does the following:

    1. Transcribe an audio file
    2. Split the transcript into segments of sentences.
    3. Get the sentiment of each segment in parallel.
    4. Combine everything into JSON.
    5. Write JSON back to object storage.

    Audio transcription is a long-running and failure-prone process.
*/

import { Sahale, sleep, succeed, fail } from 'sahale';
import startTranscription from 'startTranscription';
import getTranscription from 'getTranscription';
import getSegments from 'getSegments';
import getSentiment from 'getSentiment';
import buildResult from 'buildResult';
import uploadResult from 'uploadResult';

// This code that describes the workflow logic.
// The workflow logic is hosted serverlessly by Sahale.
// Each step in the workflow is a function that gets automatically built as its own container and hosted serverlessly by Sahale. 
const runSpeechAnalytics = async (request, ctx) => {
    const audioFileUri = request.audioFileUri;
    const numSegments = request.numSegments;

    // Start the audio transcription job.
    const startTranscriptionResponse = await startTranscription(audioFileUri);

    // Poll the transcription job every 60 seconds to check if it's complete.
    var waitingOnTranscript = true;

    let transcriptUri;

    while (waitingOnTranscript) {
        const transcription = await getTranscription(startTranscriptionResponse.jobId);
        switch (transcription.status) {
            case "SUCCESS":
                waitingOnTranscript = false;
                transcriptUri = transcription.uri
            case "FAILED":
                // Send the workflow to a failed state. This causes the workflow to exit.
                fail();
                // note: this should return something and exit the function
            case "IN_PROGRESS":
                // Sleep the workflow for 60 seconds before trying again
                sleep(60, TimeUnit.SECONDS);
                break;

        }
    }

    // Split the transcript into segments.
    const segments = await getSegments(transcriptUri, numSegments)

    // Get the sentiment of each segment in parallel.
    var promiseArray = [];
    for (const segment of segments) {
        promiseArray.push(getSentiment(segment));
    }
    const sentiments = await Promise.all(promiseArray);

    // Zip all the segments and corresponding sentiments into single file.
    const result = await buildResult(segments, sentiments);

    // Upload the result.
    await uploadResult(result);

    // Send the workflow to a success state.
    succeed();
}

// Register the activities and the workflow with Sahale.
const app = new Sahale();
app.registerActivities([
    startTranscription,
    getTranscription,
    getSegments,
    getSentiment,
    buildResult
]);
app.registerWorkflow(runSpeechAnalytics);

// Start the application in the Sahale cloud and start listening for triggers.
app.start();