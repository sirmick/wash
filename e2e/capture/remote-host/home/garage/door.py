import time

from gpiozero import Button

door = Button(17)

while True:
    state = "closed" if door.is_pressed else "open"
    print(time.strftime("%H:%M:%S"), state, flush=True)
    time.sleep(1)
