package main

import (
	"log"

	"youtubeshort/internal/app"
)

func main() {
	hideConsoleWindow()
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
