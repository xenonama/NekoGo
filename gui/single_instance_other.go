//go:build !windows

package main

import "github.com/wailsapp/wails/v3/pkg/application"

const singleInstanceScope = "NekoGo.SingleInstance.v1"

type singleInstance struct{}

func acquireSingleInstance(_ string, _ []string) (*singleInstance, error) {
	return &singleInstance{}, nil
}

func (*singleInstance) watchWindow(_ *application.WebviewWindow) {}
func (*singleInstance) Close() error                             { return nil }
