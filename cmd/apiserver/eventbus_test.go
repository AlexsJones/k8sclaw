package main

import "testing"

func TestDisabledEventBusIsActuallyNil(t *testing.T) {
	for _, url := range []string{"", " \n\t"} {
		bus, err := connectOptionalEventBus(url)
		if err != nil || bus != nil {
			t.Fatalf("disabled bus: %v %v", bus, err)
		}
	}
}
