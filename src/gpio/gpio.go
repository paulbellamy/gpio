package gpio

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"time"
)

const (
	GPIO_IN  = "in"
	GPIO_OUT = "out"
	GPIO_ON  = "1"
	GPIO_OFF = "0"
)

// GPIO numbers should be from this list
// 0, 1, 4, 7, 8, 9, 10, 11, 14, 15, 17, 18, 21, 22, 23, 24, 25

// Note that the GPIO numbers that you program here refer to the pins
// of the BCM2835 and *not* the numbers on the pin header.
// So, if you want to activate GPIO7 on the header you should be
// using GPIO4 in this script. Likewise if you want to activate GPIO0
// on the header you should be using GPIO17 here.
var GPIO_CHANNELS = []uint8{4, 17, 18, 21, 22, 23, 24, 25}

type InputPin interface {
	GetValue() (int, error)
	IsHigh() (bool, error)
	io.Closer
}

type OutputPin interface {
	SetHigh() error
	SetLow() error
	io.Closer
}

type PWMPin interface {
	// A percentage value from 0-100
	SetPWM(value int) error
	io.Closer
}

func NewInputPin(channel uint8) (InputPin, error) {
	pin := &pin{
		channel: channel,
	}
	if err := pin.init(); err != nil {
		return nil, err
	}

	if err := pin.setMode(GPIO_IN); err != nil {
		return nil, err
	}

	return pin, nil
}

func NewOutputPin(channel uint8) (OutputPin, error) {
	pin := &pin{
		channel: channel,
	}
	if err := pin.init(); err != nil {
		return nil, err
	}

	if err := pin.setMode(GPIO_OUT); err != nil {
		return nil, err
	}

	return pin, nil
}

func NewPWMPin(channel uint8) (PWMPin, error) {
	pin := &pin{
		channel: channel,
	}
	if err := pin.init(); err != nil {
		return nil, err
	}

	if err := pin.setMode(GPIO_OUT); err != nil {
		return nil, err
	}

	return pin, nil
}

type pin struct {
	channel   uint8
	valueFile *os.File

	pwmLoop     chan int
	quitPwmLoop chan chan error
}

func (p *pin) init() error {
	var err error

	if err = p.exportChannel(); err != nil {
		return err
	}

	if p.valueFile, err = os.OpenFile(fmt.Sprintf("/sys/class/gpio/gpio%d/value", p.channel), os.O_RDWR, 600); err != nil {
		return err
	}

	return nil
}

func (p *pin) exportChannel() error {
	exportFile, err := os.OpenFile("/sys/class/gpio/export", os.O_WRONLY, 200)
	if err != nil {
		return err
	}
	defer exportFile.Close()

	// if this exists we have to unexport it first
	_, err = os.Stat(fmt.Sprintf("/sys/class/gpio/gpio%d", p.channel))
	if err == nil {
		if err = p.unexportChannel(); err != nil {
			return err
		}
	} else {
		if !os.IsNotExist(err) {
			return err
		}
	}

	_, err = exportFile.WriteString(fmt.Sprintf("%d", p.channel))
	return err
}

func (p *pin) unexportChannel() error {
	unexportFile, err := os.OpenFile("/sys/class/gpio/unexport", os.O_WRONLY, 200)
	if err != nil {
		return err
	}
	defer unexportFile.Close()

	_, err = unexportFile.WriteString(fmt.Sprintf("%d", p.channel))
	return err
}

func (p *pin) GetValue() (int, error) {
	b, err := ioutil.ReadAll(p.valueFile)
	if err != nil {
		return 0, nil
	}

	return strconv.Atoi(string(b))
}

func (p *pin) IsHigh() (bool, error) {
	val, err := p.GetValue()
	return (val == 1), err
}

func (p *pin) SetHigh() error {
	_, err := p.valueFile.WriteString(GPIO_ON)
	return err
}

func (p *pin) SetLow() error {
	_, err := p.valueFile.WriteString(GPIO_OFF)
	return err
}

func valueToDuration(value int, max time.Duration) time.Duration {
	return (max / time.Duration(100)) * time.Duration(value)
}

func (p *pin) startPwmLoop(initialValue int) {
	if p.pwmLoop != nil {
		p.pwmLoop <- initialValue
		return
	}
	p.pwmLoop = make(chan int)
	p.quitPwmLoop = make(chan chan error)

	go func() {
		var err error
		var period time.Duration = 20 * time.Millisecond
		var highDuration time.Duration
		var ticker *time.Ticker = time.NewTicker(period)
		defer func() {
			// cleanup
			close(p.pwmLoop)
			close(p.quitPwmLoop)

			p.pwmLoop = nil
			p.quitPwmLoop = nil

			ticker.Stop()
		}()

		for {
			select {
			case v := <-p.pwmLoop:
				switch {
				case v == 0:
					p.SetLow()
					return
				case v == 100:
					p.SetHigh()
					return
				default:
					highDuration = valueToDuration(v, period)
				}
			case reply := <-p.quitPwmLoop:
				reply <- nil
				return
			case <-ticker.C:
				err = p.SetHigh()
				if err != nil {
					reply := <-p.quitPwmLoop
					reply <- err
					return
				}

				time.Sleep(highDuration)

				err = p.SetLow()
				if err != nil {
					reply := <-p.quitPwmLoop
					reply <- err
					return
				}
			}
		}
	}()
}

func (p *pin) setMode(mode string) error {
	directionFile, err := os.OpenFile(fmt.Sprintf("/sys/class/gpio/gpio%d/direction", p.channel), os.O_WRONLY, 200)
	if err != nil {
		return err
	}
	defer directionFile.Close()

	_, err = directionFile.WriteString(mode)
	return err
}

func (p *pin) stopPwmLoop() error {
	if p.pwmLoop == nil {
		return nil
	}
	reply := make(chan error)
	p.quitPwmLoop <- reply

	return <-reply
}

// Set the percentage of power to this pwm port from 0-100
func (p *pin) SetPWM(value int) error {
	if value == 0 {
		p.stopPwmLoop()
		p.SetLow()
	} else {
		p.startPwmLoop(value)
	}
	return nil
}

// Tear-down this pin. Cleans up exported channels, and leaves the system in a
// clean state.
func (p *pin) Close() error {
	var err error

	if err = p.stopPwmLoop(); err != nil {
		return err
	}

	if err = p.valueFile.Close(); err != nil {
		return err
	}

	return p.unexportChannel()
}
