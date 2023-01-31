from cakework import Cakework
import time

def say_hello(params):
    time.sleep(5)
    return "Hello " + params['name'] + "!"

if __name__ == "__main__":
    cakework = Cakework("react-example-backend")
    cakework.add_task(say_hello)