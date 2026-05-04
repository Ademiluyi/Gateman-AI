package doorbell

import (
	"log"
	"os"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

const gpioPin = "GPIO17"

// Detector listens for doorbell button presses via GPIO.
// Falls back to a timer stub when not running on Pi hardware.
type Detector struct{}

func New() *Detector {
	return &Detector{}
}

// Rings returns a channel that receives a value each time the doorbell fires.
func (d *Detector) Rings() <-chan struct{} {
	ch := make(chan struct{})

	if isRunningOnPi() {
		go d.listenGPIO(ch)
	} else {
		go d.listenStub(ch)
	}

	return ch
}

// listenGPIO watches GPIO17 for a button press (pin goes LOW when pressed).
func (d *Detector) listenGPIO(ch chan<- struct{}) {
	if _, err := host.Init(); err != nil {
		log.Printf("GPIO init failed: %v — falling back to stub", err)
		d.listenStub(ch)
		return
	}

	pin := gpioreg.ByName(gpioPin)
	if pin == nil {
		log.Printf("GPIO pin %s not found — falling back to stub", gpioPin)
		d.listenStub(ch)
		return
	}

	// Pull the pin HIGH internally — button press connects it to GND (LOW).
	if err := pin.In(gpio.PullUp, gpio.FallingEdge); err != nil {
		log.Printf("GPIO pin config failed: %v — falling back to stub", err)
		d.listenStub(ch)
		return
	}

	log.Printf("Doorbell listening on %s (GPIO17, physical pin 11)", gpioPin)

	for {
		// WaitForEdge blocks until the pin goes LOW (button pressed).
		pin.WaitForEdge(-1)

		if pin.Read() == gpio.Low {
			log.Println("Doorbell button pressed")
			ch <- struct{}{}
			// Debounce — ignore subsequent triggers for 2 seconds.
			time.Sleep(2 * time.Second)
		}
	}
}

// listenStub fires once after 3 seconds — for testing on non-Pi hardware.
func (d *Detector) listenStub(ch chan<- struct{}) {
	log.Println("Doorbell running in stub mode — firing once after 3 seconds")
	time.Sleep(3 * time.Second)
	ch <- struct{}{}
}

// isRunningOnPi checks if we're on Linux ARM hardware (i.e. the Pi).
func isRunningOnPi() bool {
	_, err := os.Stat("/sys/bus/platform/drivers/raspberrypi-firmware")
	return err == nil
}
