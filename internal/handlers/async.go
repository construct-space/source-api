package handlers

import "log"

func safeAsync(label string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[%s] async task panicked: %v", label, r)
			}
		}()
		fn()
	}()
}
