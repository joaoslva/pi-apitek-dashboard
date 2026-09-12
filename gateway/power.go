package main

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

// logindPower asks systemd-logind to power off or reboot. pms-gateway is
// allowed to by /etc/polkit-1/rules.d/50-pms-gateway.rules; systemd stops
// every service cleanly on the way down.
func logindPower(action string) error {
	method, ok := map[string]string{"poweroff": "PowerOff", "reboot": "Reboot"}[action]
	if !ok {
		return fmt.Errorf("unknown power action %q", action)
	}
	return callLogind(method, nil, false) // false: never ask for a password
}

// logindCan reports whether this user may do the action: "yes", "challenge"
// (would need a password), "no" or "na".
func logindCan(action string) (string, error) {
	method, ok := map[string]string{"poweroff": "CanPowerOff", "reboot": "CanReboot"}[action]
	if !ok {
		return "", fmt.Errorf("unknown power action %q", action)
	}
	var answer string
	err := callLogind(method, &answer)
	return answer, err
}

func callLogind(method string, out *string, args ...any) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	call := conn.Object("org.freedesktop.login1", "/org/freedesktop/login1").
		Call("org.freedesktop.login1.Manager."+method, 0, args...)
	if out != nil {
		return call.Store(out)
	}
	return call.Err
}
