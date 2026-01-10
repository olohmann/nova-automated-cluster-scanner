package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/olohmann/nova-automated-cluster-scanner/pkg/config"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/github"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/logging"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/metrics"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/nova"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "", "Path to configuration file")
	showVersion := flag.Bool("version", false, "Show version and exit")
	flag.Parse()

	if *showVersion {
		println("nova-scanner version:", version)
		os.Exit(0)
	}

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		println("Error loading config:", err.Error())
		os.Exit(1)
	}

	// Initialize logger
	logger := logging.NewLogger(cfg.LogLevel)
	logger.Info().
		Str("version", version).
		Bool("dry_run", cfg.DryRun).
		Bool("scan_helm", cfg.ScanHelm).
		Bool("scan_containers", cfg.ScanContainers).
		Str("min_severity", cfg.MinSeverity).
		Str("output_mode", cfg.OutputMode).
		Msg("Nova scanner starting")

	// Initialize metrics
	m := metrics.NewMetrics(cfg.PushgatewayURL, cfg.JobName)
	m.Reset() // Clear any stale version info metrics

	// Initialize scanner
	scanner, err := nova.NewScanner(cfg, logger)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to create scanner")
		os.Exit(1)
	}

	ctx := context.Background()
	var hadError bool

	// Handle markdown output mode
	if cfg.IsMarkdownMode() {
		if err := runMarkdownMode(ctx, cfg, scanner, logger); err != nil {
			logger.Error().Err(err).Msg("Failed to generate markdown output")
			os.Exit(1)
		}
		return
	}

	// GitHub mode: Initialize issue manager
	issueManager := github.NewIssueManager(
		cfg.GitHubToken,
		cfg.GitHubOwner,
		cfg.GitHubRepo,
		cfg.DryRun,
		logger,
	)

	// Track namespaces with outdated Helm releases for container deduplication
	var outdatedHelmNamespaces map[string]bool

	// Scan Helm charts
	if cfg.ScanHelm {
		result, err := scanner.ScanHelm(ctx)
		if err != nil {
			m.RecordError()
			hadError = true
		} else {
			m.RecordHelmScan(len(result.Outdated), result.Duration)

			// Get namespaces with outdated releases for container deduplication
			outdatedHelmNamespaces = result.OutdatedNamespaces()

			// Record version info metrics for all outdated releases
			for _, release := range result.Outdated {
				m.RecordHelmChartInfo(
					release.ReleaseName,
					release.Namespace,
					release.ChartName,
					release.Installed.Version,
					release.Latest.Version,
					release.Deprecated,
				)
			}

			// Create issues for outdated releases
			for _, release := range result.Outdated {
				url, err := issueManager.CreateHelmIssue(ctx, release)
				if err != nil {
					logger.Error().Err(err).
						Str("release", release.ReleaseName).
						Msg("Failed to create issue")
				} else if url != "" {
					m.RecordIssueCreated("helm")
				}
			}
		}
	}

	// Scan containers
	if cfg.ScanContainers {
		// Pass outdated Helm namespaces to skip containers that will be updated with Helm charts
		result, err := scanner.ScanContainers(ctx, outdatedHelmNamespaces)
		if err != nil {
			m.RecordError()
			hadError = true
		} else {
			m.RecordContainerScan(len(result.Outdated), result.Duration)

			// Record version info metrics for all outdated containers
			for _, container := range result.Outdated {
				m.RecordContainerInfo(
					container.Name,
					container.CurrentTag,
					container.LatestTag,
				)
			}

			// Create issues for outdated containers
			for _, container := range result.Outdated {
				url, err := issueManager.CreateContainerIssue(ctx, container)
				if err != nil {
					logger.Error().Err(err).
						Str("image", container.Name).
						Msg("Failed to create issue")
				} else if url != "" {
					m.RecordIssueCreated("container")
				}
			}
		}
	}

	// Push metrics to Pushgateway
	if cfg.PushgatewayURL != "" {
		if err := m.Push(); err != nil {
			logger.Error().Err(err).Msg("Failed to push metrics")
		} else {
			logger.MetricsPushed(cfg.PushgatewayURL)
		}
	}

	logger.Info().Msg("Nova scanner completed")

	if hadError {
		os.Exit(1)
	}
}

// runMarkdownMode handles the markdown output mode for local testing.
func runMarkdownMode(ctx context.Context, cfg *config.Config, scanner *nova.Scanner, logger *logging.Logger) error {
	var output io.Writer = os.Stdout
	if cfg.MarkdownOutput != "" {
		f, err := os.Create(cfg.MarkdownOutput)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer f.Close()
		output = f
		logger.Info().Str("file", cfg.MarkdownOutput).Msg("Writing markdown output to file")
	}

	var sb strings.Builder
	sb.WriteString("# Nova Scanner Results\n\n")
	sb.WriteString("_Preview of issues that would be created_\n\n")
	sb.WriteString("---\n\n")

	issueCount := 0
	var outdatedHelmNamespaces map[string]bool

	// Scan Helm charts
	if cfg.ScanHelm {
		result, err := scanner.ScanHelm(ctx)
		if err != nil {
			return fmt.Errorf("helm scan failed: %w", err)
		}

		// Get namespaces with outdated releases for container deduplication
		outdatedHelmNamespaces = result.OutdatedNamespaces()

		if len(result.Outdated) > 0 {
			sb.WriteString(fmt.Sprintf("## Helm Charts (%d outdated)\n\n", len(result.Outdated)))

			for _, release := range result.Outdated {
				issueCount++
				title := github.FormatHelmIssueTitle(release)
				body := github.FormatHelmIssueBody(release)

				sb.WriteString(fmt.Sprintf("### Issue %d: %s\n\n", issueCount, title))
				sb.WriteString(body)
				sb.WriteString("\n\n---\n\n")
			}
		} else {
			sb.WriteString("## Helm Charts\n\n_No outdated Helm charts found._\n\n")
		}
	}

	// Scan containers
	if cfg.ScanContainers {
		// Pass outdated Helm namespaces to skip containers that will be updated with Helm charts
		result, err := scanner.ScanContainers(ctx, outdatedHelmNamespaces)
		if err != nil {
			return fmt.Errorf("container scan failed: %w", err)
		}

		if len(result.Outdated) > 0 {
			sb.WriteString(fmt.Sprintf("## Container Images (%d outdated)\n\n", len(result.Outdated)))

			for _, container := range result.Outdated {
				issueCount++
				title := github.FormatContainerIssueTitle(container)
				body := github.FormatContainerIssueBody(container)

				sb.WriteString(fmt.Sprintf("### Issue %d: %s\n\n", issueCount, title))
				sb.WriteString(body)
				sb.WriteString("\n\n---\n\n")
			}
		} else {
			sb.WriteString("## Container Images\n\n_No outdated container images found._\n\n")
		}

		// Note skipped containers
		if len(result.Skipped) > 0 {
			sb.WriteString(fmt.Sprintf("\n_Note: %d container images were skipped because they are in namespaces with outdated Helm releases (updating the chart will update the containers)._\n\n", len(result.Skipped)))
		}
	}

	sb.WriteString(fmt.Sprintf("**Total issues that would be created: %d**\n", issueCount))

	_, err := output.Write([]byte(sb.String()))
	return err
}
