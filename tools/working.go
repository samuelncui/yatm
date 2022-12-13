package tools

import "sync"

var (
	wg sync.WaitGroup
)

func Working() {
	wg.Add(1)
}

func Done() {
	wg.Done()
}

func Wait() {
	wg.Wait()
}
