package main

import "errors"

// Brightness is not on Windows yet; the next commit adds DDC/CI and WMI.

var errNoBrightness = errors.New("brightness is not available on Windows yet")

func listMonitors() ([]monitor, error) { return nil, nil }

func platformReadBrightness(string) (int, error) { return 0, errNoBrightness }

func platformWriteBrightness(string, int) error { return errNoBrightness }

func platformForgetMonitor(string) {}
