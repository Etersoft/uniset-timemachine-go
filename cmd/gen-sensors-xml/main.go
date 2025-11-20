package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

type options struct {
	output     string
	startID    int
	count      int
	namePrefix string
	nameSuffix string
	ioType     string
	textPrefix string
}

func main() {
	opts := parseFlags()
	if opts.count < 0 {
		log.Fatalf("--count must be >= 0")
	}
	if err := generate(opts); err != nil {
		log.Fatalf("generate sensors: %v", err)
	}
}

func parseFlags() options {
	var opt options
	flag.StringVar(&opt.output, "output", "config/generated-sensors.xml", "output XML file")
	flag.IntVar(&opt.startID, "start-id", 20000, "first sensor ID")
	flag.IntVar(&opt.count, "count", 0, "number of sensors to generate")
	flag.StringVar(&opt.namePrefix, "name-prefix", "Sensor", "prefix for sensor name")
	flag.StringVar(&opt.nameSuffix, "name-suffix", "_S", "suffix for sensor name")
	flag.StringVar(&opt.ioType, "iotype", "AI", "value for iotype attribute")
	flag.StringVar(&opt.textPrefix, "text-prefix", "generated sensor", "prefix for textname")
	flag.Parse()
	return opt
}

func generate(opt options) error {
	if err := os.MkdirAll(dir(opt.output), 0o755); err != nil {
		return err
	}
	file, err := os.Create(opt.output)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := fmt.Fprintln(file, `<?xml version="1.0" encoding="utf-8"?>`); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(file, `<items>`); err != nil {
		return err
	}

	for i := 0; i < opt.count; i++ {
		id := opt.startID + i
		name := fmt.Sprintf("%s%d%s", opt.namePrefix, id, opt.nameSuffix)
		text := fmt.Sprintf("%s %d", opt.textPrefix, id)
		line := fmt.Sprintf(`	<item id="%d" iotype="%s" name="%s" textname="%s"/>`, id, opt.ioType, name, text)
		if _, err := fmt.Fprintln(file, line); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintln(file, `</items>`); err != nil {
		return err
	}
	return nil
}

func dir(path string) string {
	if idx := lastSlash(path); idx >= 0 {
		return path[:idx]
	}
	return "."
}

func lastSlash(path string) int {
	last := -1
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			last = i
		}
	}
	return last
}
