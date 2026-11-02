// Package config owns trusted process configuration for the production Backend.
package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	SingleUserLocalProfileName = "single_user_local_v1"
	defaultTenantID            = "tenant-local"
	defaultActorUserID         = "user-local"
	defaultWebAddress          = "127.0.0.1:8080"
)

type LookupEnv func(string) (string, bool)

type SingleUserLocalProfile struct {
	Name          string
	TenantID      string
	ActorUserID   string
	WebAddress    string
	Containerized bool
}

func LoadSingleUserLocalProfile(lookup LookupEnv) (SingleUserLocalProfile, error) {
	if lookup == nil {
		return SingleUserLocalProfile{}, fmt.Errorf("environment lookup is required")
	}
	containerized, err := serverBool(lookup, "SWITCHBACK_CONTAINERIZED", false)
	if err != nil {
		return SingleUserLocalProfile{}, err
	}
	profile := SingleUserLocalProfile{
		Name:          SingleUserLocalProfileName,
		TenantID:      serverValue(lookup, "SWITCHBACK_LOCAL_TENANT_ID", defaultTenantID),
		ActorUserID:   serverValue(lookup, "SWITCHBACK_LOCAL_ACTOR_ID", defaultActorUserID),
		WebAddress:    serverValue(lookup, "SWITCHBACK_WEB_ADDRESS", defaultWebAddress),
		Containerized: containerized,
	}
	if err := validateIdentity("tenant id", profile.TenantID); err != nil {
		return SingleUserLocalProfile{}, err
	}
	if err := validateIdentity("actor user id", profile.ActorUserID); err != nil {
		return SingleUserLocalProfile{}, err
	}
	if err := validateWebAddress(profile.WebAddress, profile.Containerized); err != nil {
		return SingleUserLocalProfile{}, err
	}
	return profile, nil
}

func serverValue(lookup LookupEnv, name, fallback string) string {
	if value, ok := lookup(name); ok {
		return value
	}
	return fallback
}

func serverBool(lookup LookupEnv, name string, fallback bool) (bool, error) {
	value, ok := lookup(name)
	if !ok {
		return fallback, nil
	}
	switch value {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be 0 or 1", name)
	}
}

func validateIdentity(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 160 || strings.ContainsAny(value, " \t\r\n/\\\x00") {
		return fmt.Errorf("%s must be a canonical identity", name)
	}
	return nil
}

func validateWebAddress(address string, containerized bool) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("Web address must be host:port: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || (containerized && !ip.Equal(net.IPv4zero)) || (!containerized && !ip.IsLoopback()) {
		return fmt.Errorf("Web address must use the trusted profile host")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("Web address must use a valid nonzero port")
	}
	return nil
}
