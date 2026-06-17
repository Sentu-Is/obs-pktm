//go:build windows

package timer

import (
	"fmt"
	"sync"
	"time"
)

type Options struct {
	Duration time.Duration
}

type Overlay struct {
	mu sync.Mutex

	duration time.Duration
	rtss     *rtssOSD
	stop     chan struct{}
	onFinish func()
	running  bool
}

func NewOverlay(options Options) (*Overlay, error) {
	if options.Duration <= 0 {
		options.Duration = 10 * time.Minute
	}

	return &Overlay{
		duration: options.Duration,
	}, nil
}

func (o *Overlay) SetOnFinish(fn func()) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.onFinish = fn
}

func (o *Overlay) Start() error {
	o.Stop()

	client, err := newRTSSOSD(rtssOwner)
	if err != nil {
		return err
	}

	secondsLeft := int(o.duration.Seconds())
	if err := client.Update(formatCountdown(secondsLeft)); err != nil {
		client.Release()
		return err
	}

	stop := make(chan struct{})

	o.mu.Lock()
	o.rtss = client
	o.stop = stop
	o.running = true
	o.mu.Unlock()

	go o.run(client, stop, secondsLeft)
	return nil
}

func (o *Overlay) Stop() {
	o.mu.Lock()
	client := o.rtss
	stop := o.stop
	o.rtss = nil
	o.stop = nil
	o.running = false
	o.mu.Unlock()

	if stop != nil {
		close(stop)
	}
	if client != nil {
		client.Release()
	}
}

func (o *Overlay) Close() {
	if o == nil {
		return
	}
	o.Stop()
}

func (o *Overlay) run(client *rtssOSD, stop <-chan struct{}, secondsLeft int) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			secondsLeft--
			if secondsLeft <= 0 {
				o.mu.Lock()
				current := o.rtss == client
				var onFinish func()
				if o.rtss == client {
					o.running = false
					onFinish = o.onFinish
				}
				o.mu.Unlock()

				_ = client.Update(waitingText())
				if current && onFinish != nil {
					go onFinish()
				}
				return
			}

			_ = client.Update(formatCountdown(secondsLeft))
		}
	}
}

func formatCountdown(secondsLeft int) string {
	mins := secondsLeft / 60
	secs := secondsLeft % 60
	return fmt.Sprintf("%02d:%02d", mins, secs)
}

func waitingText() string {
	return "<S1=520><C0=FF3030><C0><S1>RESET<S><C>\n<S1=420><C0=FFFFFF><C0><S1>WAITING<S><C>"
}
