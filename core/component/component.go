// Copyright © 2017 The Things Network
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package component contains code that is shared by all components (discovery, router, broker, networkserver, handler)
package component

import (
	"crypto/ecdsa"
	"crypto/tls"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/TheThingsNetwork/go-account-lib/claims"
	"github.com/TheThingsNetwork/go-account-lib/tokenkey"
	pb_discovery "github.com/TheThingsNetwork/ttn/api/discovery"
	pb_monitor "github.com/TheThingsNetwork/ttn/api/monitor"
	"github.com/apex/log"
	"github.com/spf13/viper"
	"golang.org/x/net/context" // See https://github.com/grpc/grpc-go/issues/711"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
)

// Component contains the common attributes for all TTN components
type Component struct {
	Config           Config
	Identity         *pb_discovery.Announcement
	Discovery        pb_discovery.Client
	Monitors         pb_monitor.Registry
	Ctx              log.Interface
	AccessToken      string
	privateKey       *ecdsa.PrivateKey
	tlsConfig        *tls.Config
	TokenKeyProvider tokenkey.Provider
	status           int64
	healthServer     *health.Server
}

type Interface interface {
	RegisterRPC(s *grpc.Server)
	Init(c *Component) error
	Shutdown()
	ValidateNetworkContext(ctx context.Context) (*pb_discovery.Announcement, error)
	ValidateTTNAuthContext(ctx context.Context) (*claims.Claims, error)
}

type ManagementInterface interface {
	RegisterManager(s *grpc.Server)
}

// New creates a new Component
func New(ctx log.Interface, serviceName string, announcedAddress string) (*Component, error) {
	go func() {
		memstats := new(runtime.MemStats)
		for range time.Tick(time.Minute) {
			runtime.ReadMemStats(memstats)
			ctx.WithFields(log.Fields{
				"Goroutines": runtime.NumGoroutine(),
				"Memory":     float64(memstats.Alloc) / 1000000,
			}).Debugf("Stats")
		}
	}()

	// Disable gRPC tracing
	// SEE: https://github.com/grpc/grpc-go/issues/695
	grpc.EnableTracing = false

	component := &Component{
		Config: ConfigFromViper(),
		Ctx:    ctx,
		Identity: &pb_discovery.Announcement{
			Id:             viper.GetString("id"),
			Description:    viper.GetString("description"),
			ServiceName:    serviceName,
			ServiceVersion: fmt.Sprintf("%s-%s (%s)", viper.GetString("version"), viper.GetString("gitCommit"), viper.GetString("buildDate")),
			NetAddress:     announcedAddress,
			Public:         viper.GetBool("public"),
		},
		AccessToken: viper.GetString("auth-token"),
	}

	if err := component.InitAuth(); err != nil {
		return nil, err
	}

	if serviceName != "discovery" && serviceName != "networkserver" {
		var err error
		component.Discovery, err = pb_discovery.NewClient(
			viper.GetString("discovery-address"),
			component.Identity,
			func() string {
				token, _ := component.BuildJWT()
				return token
			},
		)
		if err != nil {
			return nil, err
		}
	}

	if healthPort := viper.GetInt("health-port"); healthPort > 0 {
		http.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
			switch component.GetStatus() {
			case StatusHealthy:
				w.WriteHeader(200)
				w.Write([]byte("Status is HEALTHY"))
				return
			case StatusUnhealthy:
				w.WriteHeader(503)
				w.Write([]byte("Status is UNHEALTHY"))
				return
			}
		})
		go http.ListenAndServe(fmt.Sprintf(":%d", healthPort), nil)
	}

	component.Monitors = pb_monitor.NewRegistry(ctx)
	for name, addr := range viper.GetStringMapString("monitor-servers") {
		go component.Monitors.InitClient(name, addr)
	}

	return component, nil
}
