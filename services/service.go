package services

import (
	"context"
	"github.com/forta-protocol/forta-node/ens"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"

	"github.com/forta-protocol/forta-node/config"
)

type Service interface {
	Start() error
	Stop() error
	Name() string
}

var execIDKey = struct{}{}

func ExecID(ctx context.Context) string {
	execID := ctx.Value(execIDKey)
	if execID == nil {
		panic("cannot get exec ID")
	}
	return execID.(string)
}

func initExecID(ctx context.Context) context.Context {
	execID, err := uuid.NewUUID()
	if err != nil {
		panic(err)
	}
	return context.WithValue(ctx, execIDKey, execID.String())
}

func setContracts(cfg *config.Config) error {
	contracts, err := ens.ResolveFortaContracts(cfg.ENSConfig.JsonRpc.Url, cfg.ENSConfig.ContractAddress)
	if err != nil {
		return err
	}
	if cfg.Registry.ContractAddress == "" {
		cfg.Registry.ContractAddress = contracts.Dispatch
	}
	if cfg.Publish.ContractAddress == "" {
		cfg.Publish.ContractAddress = contracts.Alerts
	}
	cfg.AgentRegistryContractAddress = contracts.Agent
	return nil
}

func ContainerMain(name string, getServices func(ctx context.Context, cfg config.Config) ([]Service, error)) {
	cfg, err := config.GetConfigForContainer()
	if err != nil {
		log.Errorf("could not initialize log level: %v", err)
		return
	}

	if err := setContracts(&cfg); err != nil {
		log.WithError(err).Error("could not initialize contracts for config")
	}

	lvl, err := log.ParseLevel(cfg.Log.Level)
	if err != nil {
		log.Errorf("could not initialize log level: %v", err)
		return
	}
	log.SetLevel(lvl)
	log.Infof("Starting %s", name)

	ctx, cancel := InitMainContext()
	defer cancel()

	serviceList, err := getServices(ctx, cfg)
	if err != nil {
		log.Errorf("could not initialize services: %v", err)
		return
	}

	if err := StartServices(ctx, serviceList); err != nil {
		log.Error("error running services: ", err)
	}

	log.Infof("Stopping %s", name)
}

func InitMainContext() (context.Context, context.CancelFunc) {
	execIDCtx := initExecID(context.Background())
	ctx, cancel := context.WithCancel(execIDCtx)
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)
	go func() {
		<-sigc
		cancel()
	}()
	return ctx, cancel
}

// StartServices kicks off all services and blocks until an error is returned or context ends
func StartServices(ctx context.Context, services []Service) error {
	grp, ctx := errgroup.WithContext(ctx)

	for _, service := range services {
		grp.Go(service.Start)
	}

	// wait for context to stop (service.Start may either block or be async)
	grp.Go(func() error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	// clean up all services
	defer func() {
		for _, service := range services {
			if err := service.Stop(); err != nil {
				log.Errorf("error stopping %s: %s", service.Name(), err.Error())
			}
		}
	}()

	if err := grp.Wait(); err != nil && err != context.Canceled {
		log.Errorf("Error returned from grp: %s", err.Error())
		return err
	}
	return nil
}
