package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"github.com/sympozium-ai/sympozium/internal/cellnreview"
	"k8s.io/apimachinery/pkg/types"
)

type issuerServiceConfig struct {
	APIVersion        string `json:"apiVersion"`
	Listen            string `json:"listen"`
	CertificateFile   string `json:"certificateFile"`
	PrivateKeyFile    string `json:"privateKeyFile"`
	TokenFile         string `json:"tokenFile"`
	Binary            string `json:"cellnBinary"`
	PolicyRoot        string `json:"policyRoot"`
	ComposerPublisher string `json:"composerPublisher"`
	ProfileLifetimeMS uint64 `json:"profileLifetimeMs"`
	SweepIntervalMS   uint64 `json:"sweepIntervalMs"`
	Bindings          []struct {
		Agent          types.NamespacedName `json:"agent"`
		OperatorGrants types.NamespacedName `json:"operatorGrants"`
		RuntimeGrants  types.NamespacedName `json:"runtimeGrants"`
		AgentGrants    types.NamespacedName `json:"agentGrants"`
		ModelPolicy    types.NamespacedName `json:"modelPolicy"`
	} `json:"bindings"`
}

func readIssuerServiceConfig(path string) (issuerServiceConfig, error) {
	var config issuerServiceConfig
	if !filepath.IsAbs(path) {
		return config, fmt.Errorf("absolute operator config path required")
	}
	f, err := os.Open(path)
	if err != nil {
		return config, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return config, err
	}
	if len(data) > 1<<20 {
		return config, fmt.Errorf("issuer config exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&config); err != nil {
		return config, err
	}
	if d.Decode(new(any)) != io.EOF {
		return config, fmt.Errorf("trailing issuer config JSON")
	}
	if config.APIVersion != "sympozium.ai/celln-issuer-service-v1" || config.ProfileLifetimeMS < 1 || config.ProfileLifetimeMS > 300000 || config.SweepIntervalMS < 1000 || config.SweepIntervalMS > 30000 || len(config.Bindings) == 0 || len(config.Bindings) > 1024 {
		return config, fmt.Errorf("invalid bounded issuer configuration")
	}
	for _, path := range []string{config.CertificateFile, config.PrivateKeyFile, config.TokenFile, config.Binary, config.PolicyRoot} {
		if !filepath.IsAbs(path) {
			return config, fmt.Errorf("absolute operator paths required")
		}
	}
	if _, _, err := net.SplitHostPort(config.Listen); err != nil {
		return config, fmt.Errorf("explicit issuer host:port required")
	}
	seen := make(map[types.NamespacedName]bool)
	for _, b := range config.Bindings {
		if b.Agent.Namespace == "" || b.Agent.Name == "" || seen[b.Agent] {
			return config, fmt.Errorf("distinct namespaced Agent bindings required")
		}
		seen[b.Agent] = true
		sources := make(map[types.NamespacedName]bool)
		for _, source := range []types.NamespacedName{b.OperatorGrants, b.RuntimeGrants, b.AgentGrants, b.ModelPolicy} {
			if source.Namespace == "" || source.Name == "" || sources[source] {
				return config, fmt.Errorf("four distinct configured authority sources required")
			}
			sources[source] = true
		}
	}
	return config, nil
}

func newCellnIssuerServiceCmd() *cobra.Command {
	var configFile string
	cmd := &cobra.Command{Use: "serve-issuer", Args: cobra.NoArgs, SilenceUsage: true,
		Short: "Run the TLS-authenticated host-local issuer with startup recovery and periodic withdrawal",
		RunE: func(cmd *cobra.Command, _ []string) error {
			config, err := readIssuerServiceConfig(configFile)
			if err != nil {
				return err
			}
			loaders := make(map[types.NamespacedName]cellnauthority.ModelLoader)
			for _, binding := range config.Bindings {
				if _, duplicate := loaders[binding.Agent]; duplicate {
					return fmt.Errorf("duplicate Agent issuer binding")
				}
				loaders[binding.Agent] = cellnauthority.ModelLoader{Selection: cellnauthority.Loader{Reader: k8sClient, OperatorSource: binding.OperatorGrants, RuntimeSource: binding.RuntimeGrants, AgentSource: binding.AgentGrants}, Source: binding.ModelPolicy}
			}
			managed, err := cellnreview.NewManagedIssuer(cellnreview.IssueOptions{Binary: config.Binary, PolicyRoot: config.PolicyRoot, ComposerPublisher: config.ComposerPublisher, ProfileLifetime: time.Duration(config.ProfileLifetimeMS) * time.Millisecond}, loaders, time.Duration(config.SweepIntervalMS)*time.Millisecond)
			if err != nil {
				return err
			}
			listener, err := net.Listen("tcp", config.Listen)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return cellnreview.ServeIssuer(ctx, listener, managed, config.TokenFile, config.CertificateFile, config.PrivateKeyFile)
		}}
	cmd.Flags().StringVar(&configFile, "config", "", "Absolute operator-owned issuer service JSON configuration")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}
