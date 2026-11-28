package main

import "os"

type selfUpdatePlan struct {
	Version      string `json:"version"`
	Source       string `json:"source"`
	SourceSize   int64  `json:"sourceSize"`
	SourceSHA256 string `json:"sourceSHA256"`
	Target       string `json:"target"`
	ReadyPath    string `json:"readyPath"`
	ResultPath   string `json:"resultPath"`
	ParentPID    int    `json:"parentPid"`
	ConnectID    string `json:"connectId,omitempty"`
}

var selfUpdateExit = func(code int) { os.Exit(code) }
