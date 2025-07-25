// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License 2.0;
// you may not use this file except in compliance with the Elastic License 2.0.

package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/elastic/elastic-agent/internal/pkg/agent/application/enroll"
	"github.com/elastic/elastic-agent/internal/pkg/agent/application/info"
	"github.com/elastic/elastic-agent/internal/pkg/agent/application/paths"
	"github.com/elastic/elastic-agent/internal/pkg/agent/configuration"
	"github.com/elastic/elastic-agent/internal/pkg/agent/errors"
	"github.com/elastic/elastic-agent/internal/pkg/agent/storage"
	"github.com/elastic/elastic-agent/internal/pkg/cli"
	"github.com/elastic/elastic-agent/internal/pkg/config"
	"github.com/elastic/elastic-agent/pkg/core/logger"
	"github.com/elastic/elastic-agent/pkg/utils"
)

var UserOwnerMismatchError = errors.New("the command is executed as root but the program files are not owned by the root user. execute the command as the user that owns the program files")

const (
	fromInstallArg      = "from-install"
	fromInstallUserArg  = "from-install-user"
	fromInstallGroupArg = "from-install-group"
)

func newEnrollCommandWithArgs(_ []string, streams *cli.IOStreams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Enroll the Elastic Agent into Fleet",
		Long:  "This command will enroll the Elastic Agent into Fleet.",
		Run: func(c *cobra.Command, args []string) {
			if err := doEnroll(streams, c); err != nil {
				fmt.Fprintf(streams.Err, "Error: %v\n%s\n", err, troubleshootMessage())
				logExternal(fmt.Sprintf("%s enroll failed: %s", paths.BinaryName, err))
				os.Exit(1)
			}
		},
	}

	addEnrollFlags(cmd)
	cmd.Flags().BoolP("force", "f", false, "Force overwrite the current and do not prompt for confirmation")

	// used by install command
	cmd.Flags().BoolP(fromInstallArg, "", false, "Set by install command to signal this was executed from install")
	cmd.Flags().MarkHidden(fromInstallArg) //nolint:errcheck //not required

	// platform specific flags
	addPlatformFlags(cmd)

	return cmd
}

func addEnrollFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("url", "", "", "URL to enroll Agent into Fleet")
	cmd.Flags().StringP("enrollment-token", "t", "", "Enrollment token to use to enroll Agent into Fleet")
	cmd.Flags().StringP("id", "", "", "Agent ID to use for enrollment into Fleet")
	cmd.Flags().StringP("replace-token", "", "", "Replace token that that allows the Agent to be replace with the provided --id as long as this token matches")
	cmd.Flags().StringP("fleet-server-es", "", "", "Start and run a Fleet Server alongside this Elastic Agent connecting to the provided Elasticsearch")
	cmd.Flags().StringP("fleet-server-es-ca", "", "", "Path to certificate authority for Fleet Server to use to communicate with Elasticsearch")
	cmd.Flags().StringP("fleet-server-es-ca-trusted-fingerprint", "", "", "Elasticsearch certificate authority's SHA256 fingerprint for Fleet Server to use")
	cmd.Flags().BoolP("fleet-server-es-insecure", "", false, "Disables validation of Elasticsearch certificates for Fleet Server")
	cmd.Flags().StringP("fleet-server-es-cert", "", "", "Client certificate for Fleet Server to use when connecting to Elasticsearch")
	cmd.Flags().StringP("fleet-server-es-cert-key", "", "", "Client private key for Fleet Server to use when connecting to Elasticsearch")
	cmd.Flags().StringP("fleet-server-service-token", "", "", "Service token for Fleet Server to use for communication with Elasticsearch")
	cmd.Flags().StringP("fleet-server-service-token-path", "", "", "Filepath for the service token secret file used by Fleet Server for communication with Elasticsearch")
	cmd.Flags().StringP("fleet-server-policy", "", "", "Start and run a Fleet Server on this specific policy")
	cmd.Flags().StringP("fleet-server-host", "", "", "Fleet Server HTTP binding host (overrides the policy)")
	cmd.Flags().Uint16P("fleet-server-port", "", 0, "Fleet Server HTTP binding port (overrides the policy)")
	cmd.Flags().StringP("fleet-server-cert", "", "", "Certificate for Fleet Server to use for exposed HTTPS endpoint")
	cmd.Flags().StringP("fleet-server-cert-key", "", "", "Private key for the certificate used by Fleet Server for exposed HTTPS endpoint")
	cmd.Flags().StringP("fleet-server-cert-key-passphrase", "", "", "Path for private key passphrase file used to decrypt Fleet Server certificate key")
	cmd.Flags().StringP("fleet-server-client-auth", "", "none", "Fleet Server mTLS client authentication for connecting Elastic Agents. Must be one of [none, optional, required]")
	cmd.Flags().StringSliceP("header", "", []string{}, "Headers used by Agent to communicate with Fleet Server, and when a bootstrapped Fleet Server communicates with Elasticsearch")
	cmd.Flags().BoolP("fleet-server-insecure-http", "", false, "Expose Fleet Server over HTTP (not recommended; insecure)")
	cmd.Flags().StringP("certificate-authorities", "a", "", "Comma-separated list of root certificates for server verification used by Elastic Agent and Fleet Server")
	cmd.Flags().StringP("ca-sha256", "p", "", "Comma-separated list of certificate authority hash pins for server verification used by Elastic Agent and Fleet Server")
	cmd.Flags().StringP("elastic-agent-cert", "", "", "Elastic Agent client certificate to use with Fleet Server during mTLS authentication")
	cmd.Flags().StringP("elastic-agent-cert-key", "", "", "Elastic Agent client private key to use with Fleet Server during mTLS authentication")
	cmd.Flags().StringP("elastic-agent-cert-key-passphrase", "", "", "Path for private key passphrase file used to decrypt Elastic Agent client certificate key")
	cmd.Flags().BoolP("insecure", "i", false, "Allow insecure connection made by the Elastic Agent. It's also required to use a Fleet Server on a HTTP endpoint")
	cmd.Flags().StringP("staging", "", "", "Configures Elastic Agent to download artifacts from a staging build")
	cmd.Flags().StringP("proxy-url", "", "", "Configures the proxy URL: when bootstrapping Fleet Server, it's the proxy used by Fleet Server to connect to Elasticsearch; when enrolling the Elastic Agent to Fleet Server, it's the proxy used by the Elastic Agent to connect to Fleet Server")
	cmd.Flags().BoolP("proxy-disabled", "", false, "Disable proxy support including environment variables: when bootstrapping Fleet Server, it's the proxy used by Fleet Server to connect to Elasticsearch; when enrolling the Elastic Agent to Fleet Server, it's the proxy used by the Elastic Agent to connect to Fleet Server")
	cmd.Flags().StringSliceP("proxy-header", "", []string{}, "Proxy headers used with CONNECT request: when bootstrapping Fleet Server, it's the proxy used by Fleet Server to connect to Elasticsearch; when enrolling the Elastic Agent to Fleet Server, it's the proxy used by the Elastic Agent to connect to Fleet Server")
	cmd.Flags().BoolP("delay-enroll", "", false, "Delays enrollment to occur on first start of the Elastic Agent service")
	cmd.Flags().DurationP("daemon-timeout", "", 0, "Timeout waiting for Elastic Agent daemon")
	cmd.Flags().DurationP("enroll-timeout", "", 10*time.Minute, "Timeout waiting for Elastic Agent enroll command. A negative value disables the timeout.")
	cmd.Flags().DurationP("fleet-server-timeout", "", 0, "When bootstrapping Fleet Server, timeout waiting for Fleet Server to be ready to start enrollment")
	cmd.Flags().Bool("skip-daemon-reload", false, "Skip daemon reload after enrolling")
	cmd.Flags().StringSliceP("tag", "", []string{}, "User-set tags")

	cmd.Flags().MarkHidden("skip-daemon-reload") //nolint:errcheck // an error is only returned if the flag does not exist.
}

func validateEnrollFlags(cmd *cobra.Command) error {
	ca, _ := cmd.Flags().GetString("certificate-authorities")
	if ca != "" && !filepath.IsAbs(ca) {
		return errors.New("--certificate-authorities must be provided as an absolute path", errors.M("path", ca), errors.TypeConfig)
	}
	cert, _ := cmd.Flags().GetString("elastic-agent-cert")
	if cert != "" && !filepath.IsAbs(cert) {
		return errors.New("--elastic-agent-cert must be provided as an absolute path", errors.M("path", cert), errors.TypeConfig)
	}
	key, _ := cmd.Flags().GetString("elastic-agent-cert-key")
	if key != "" && !filepath.IsAbs(key) {
		return errors.New("--elastic-agent-cert-key must be provided as an absolute path", errors.M("path", key), errors.TypeConfig)
	}
	keyPassphrase, _ := cmd.Flags().GetString("elastic-agent-cert-key-passphrase")
	if keyPassphrase != "" {
		if !filepath.IsAbs(keyPassphrase) {
			return errors.New("--elastic-agent-cert-key-passphrase must be provided as an absolute path", errors.M("path", keyPassphrase), errors.TypeConfig)
		}

		if cert == "" || key == "" {
			return errors.New("--elastic-agent-cert and --elastic-agent-cert-key must be provided when using --elastic-agent-cert-key-passphrase", errors.M("path", keyPassphrase), errors.TypeConfig)
		}
	}
	esCa, _ := cmd.Flags().GetString("fleet-server-es-ca")
	if esCa != "" && !filepath.IsAbs(esCa) {
		return errors.New("--fleet-server-es-ca must be provided as an absolute path", errors.M("path", esCa), errors.TypeConfig)
	}
	esCert, _ := cmd.Flags().GetString("fleet-server-es-cert")
	if esCert != "" && !filepath.IsAbs(esCert) {
		return errors.New("--fleet-server-es-cert must be provided as an absolute path", errors.M("path", esCert), errors.TypeConfig)
	}
	esCertKey, _ := cmd.Flags().GetString("fleet-server-es-cert-key")
	if esCertKey != "" && !filepath.IsAbs(esCertKey) {
		return errors.New("--fleet-server-es-cert-key must be provided as an absolute path", errors.M("path", esCertKey), errors.TypeConfig)
	}
	fCert, _ := cmd.Flags().GetString("fleet-server-cert")
	if fCert != "" && !filepath.IsAbs(fCert) {
		return errors.New("--fleet-server-cert must be provided as an absolute path", errors.M("path", fCert), errors.TypeConfig)
	}
	fCertKey, _ := cmd.Flags().GetString("fleet-server-cert-key")
	if fCertKey != "" && !filepath.IsAbs(fCertKey) {
		return errors.New("--fleet-server-cert-key must be provided as an absolute path", errors.M("path", fCertKey), errors.TypeConfig)
	}
	fTokenPath, _ := cmd.Flags().GetString("fleet-server-service-token-path")
	if fTokenPath != "" && !filepath.IsAbs(fTokenPath) {
		return errors.New("--fleet-server-service-token-path must be provided as an absolute path", errors.M("path", fTokenPath), errors.TypeConfig)
	}
	fToken, _ := cmd.Flags().GetString("fleet-server-service-token")
	if fToken != "" && fTokenPath != "" {
		return errors.New("--fleet-server-service-token and --fleet-server-service-token-path are mutually exclusive", errors.TypeConfig)
	}
	fPassphrase, _ := cmd.Flags().GetString("fleet-server-cert-key-passphrase")
	if fPassphrase != "" && !filepath.IsAbs(fPassphrase) {
		return errors.New("--fleet-server-cert-key-passphrase must be provided as an absolute path", errors.M("path", fPassphrase), errors.TypeConfig)
	}
	fClientAuth, _ := cmd.Flags().GetString("fleet-server-client-auth")
	switch fClientAuth {
	case "none", "optional", "required":
		// NOTE we can split this case if we want to do additional checks when optional or required is passed.
	default:
		return errors.New("--fleet-server-client-auth must be one of [none, optional, required]")
	}
	return nil
}

func buildEnrollmentFlags(cmd *cobra.Command, url string, token string) []string {
	if url == "" {
		url, _ = cmd.Flags().GetString("url")
	}
	if token == "" {
		token, _ = cmd.Flags().GetString("enrollment-token")
	}
	id, _ := cmd.Flags().GetString("id")
	replaceToken, _ := cmd.Flags().GetString("replace-token")
	fServer, _ := cmd.Flags().GetString("fleet-server-es")
	fElasticSearchCA, _ := cmd.Flags().GetString("fleet-server-es-ca")
	fElasticSearchCASHA256, _ := cmd.Flags().GetString("fleet-server-es-ca-trusted-fingerprint")
	fElasticSearchInsecure, _ := cmd.Flags().GetBool("fleet-server-es-insecure")
	fElasticSearchClientCert, _ := cmd.Flags().GetString("fleet-server-es-cert")
	fElasticSearchClientCertKey, _ := cmd.Flags().GetString("fleet-server-es-cert-key")
	fServiceToken, _ := cmd.Flags().GetString("fleet-server-service-token")
	fServiceTokenPath, _ := cmd.Flags().GetString("fleet-server-service-token-path")
	fPolicy, _ := cmd.Flags().GetString("fleet-server-policy")
	fHost, _ := cmd.Flags().GetString("fleet-server-host")
	fPort, _ := cmd.Flags().GetUint16("fleet-server-port")
	fCert, _ := cmd.Flags().GetString("fleet-server-cert")
	fCertKey, _ := cmd.Flags().GetString("fleet-server-cert-key")
	fPassphrase, _ := cmd.Flags().GetString("fleet-server-cert-key-passphrase")
	fClientAuth, _ := cmd.Flags().GetString("fleet-server-client-auth")
	fHeaders, _ := cmd.Flags().GetStringSlice("header")
	fInsecure, _ := cmd.Flags().GetBool("fleet-server-insecure-http")
	ca, _ := cmd.Flags().GetString("certificate-authorities")
	cert, _ := cmd.Flags().GetString("elastic-agent-cert")
	key, _ := cmd.Flags().GetString("elastic-agent-cert-key")
	keyPassphrase, _ := cmd.Flags().GetString("elastic-agent-cert-key-passphrase")
	sha256, _ := cmd.Flags().GetString("ca-sha256")
	insecure, _ := cmd.Flags().GetBool("insecure")
	staging, _ := cmd.Flags().GetString("staging")
	fProxyURL, _ := cmd.Flags().GetString("proxy-url")
	fProxyDisabled, _ := cmd.Flags().GetBool("proxy-disabled")
	fProxyHeaders, _ := cmd.Flags().GetStringSlice("proxy-header")
	delayEnroll, _ := cmd.Flags().GetBool("delay-enroll")
	daemonTimeout, _ := cmd.Flags().GetDuration("daemon-timeout")
	enrollTimeout, _ := cmd.Flags().GetDuration("enroll-timeout")
	fTimeout, _ := cmd.Flags().GetDuration("fleet-server-timeout")
	skipDaemonReload, _ := cmd.Flags().GetBool("skip-daemon-reload")
	fTags, _ := cmd.Flags().GetStringSlice("tag")
	args := []string{}
	if url != "" {
		args = append(args, "--url")
		args = append(args, url)
	}
	if token != "" {
		args = append(args, "--enrollment-token")
		args = append(args, token)
	}
	if id != "" {
		args = append(args, "--id")
		args = append(args, id)
	}
	if replaceToken != "" {
		args = append(args, "--replace-token")
		args = append(args, replaceToken)
	}
	if fServer != "" {
		args = append(args, "--fleet-server-es")
		args = append(args, fServer)
	}
	if fElasticSearchCA != "" {
		args = append(args, "--fleet-server-es-ca")
		args = append(args, fElasticSearchCA)
	}
	if fElasticSearchCASHA256 != "" {
		args = append(args, "--fleet-server-es-ca-trusted-fingerprint")
		args = append(args, fElasticSearchCASHA256)
	}
	if fElasticSearchClientCert != "" {
		args = append(args, "--fleet-server-es-cert")
		args = append(args, fElasticSearchClientCert)
	}
	if fElasticSearchClientCertKey != "" {
		args = append(args, "--fleet-server-es-cert-key")
		args = append(args, fElasticSearchClientCertKey)
	}
	if fServiceToken != "" {
		args = append(args, "--fleet-server-service-token")
		args = append(args, fServiceToken)
	}
	if fServiceTokenPath != "" {
		args = append(args, "--fleet-server-service-token-path")
		args = append(args, fServiceTokenPath)
	}
	if fPolicy != "" {
		args = append(args, "--fleet-server-policy")
		args = append(args, fPolicy)
	}
	if fHost != "" {
		args = append(args, "--fleet-server-host")
		args = append(args, fHost)
	}
	if fPort > 0 {
		args = append(args, "--fleet-server-port")
		args = append(args, strconv.Itoa(int(fPort)))
	}
	if fCert != "" {
		args = append(args, "--fleet-server-cert")
		args = append(args, fCert)
	}
	if fCertKey != "" {
		args = append(args, "--fleet-server-cert-key")
		args = append(args, fCertKey)
	}
	if fPassphrase != "" {
		args = append(args, "--fleet-server-cert-key-passphrase")
		args = append(args, fPassphrase)
	}
	if fClientAuth != "" {
		args = append(args, "--fleet-server-client-auth")
		args = append(args, fClientAuth)
	}
	if daemonTimeout != 0 {
		args = append(args, "--daemon-timeout")
		args = append(args, daemonTimeout.String())
	}
	if enrollTimeout != 0 {
		args = append(args, "--enroll-timeout")
		args = append(args, enrollTimeout.String())
	}
	if fTimeout != 0 {
		args = append(args, "--fleet-server-timeout")
		args = append(args, fTimeout.String())
	}

	for k, v := range mapFromEnvList(fHeaders) {
		args = append(args, "--header")
		args = append(args, k+"="+v)
	}

	if fInsecure {
		args = append(args, "--fleet-server-insecure-http")
	}
	if ca != "" {
		args = append(args, "--certificate-authorities")
		args = append(args, ca)
	}
	if cert != "" {
		args = append(args, "--elastic-agent-cert")
		args = append(args, cert)
	}
	if key != "" {
		args = append(args, "--elastic-agent-cert-key")
		args = append(args, key)
	}
	if keyPassphrase != "" {
		args = append(args, "--elastic-agent-cert-key-passphrase")
		args = append(args, keyPassphrase)
	}
	if sha256 != "" {
		args = append(args, "--ca-sha256")
		args = append(args, sha256)
	}
	if insecure {
		args = append(args, "--insecure")
	}
	if staging != "" {
		args = append(args, "--staging")
		args = append(args, staging)
	}

	if fProxyURL != "" {
		args = append(args, "--proxy-url")
		args = append(args, fProxyURL)
	}
	if fProxyDisabled {
		args = append(args, "--proxy-disabled")
		args = append(args, "true")
	}
	for k, v := range mapFromEnvList(fProxyHeaders) {
		args = append(args, "--proxy-header")
		args = append(args, k+"="+v)
	}

	if delayEnroll {
		args = append(args, "--delay-enroll")
	}

	if fElasticSearchInsecure {
		args = append(args, "--fleet-server-es-insecure")
	}

	if skipDaemonReload {
		args = append(args, "--skip-daemon-reload")
	}
	for _, v := range fTags {
		args = append(args, "--tag", v)
	}
	return args
}

func doEnroll(streams *cli.IOStreams, cmd *cobra.Command) error {
	err := validateEnrollFlags(cmd)
	if err != nil {
		return err
	}

	fromInstall, _ := cmd.Flags().GetBool(fromInstallArg)

	hasRoot, err := utils.HasRoot()
	if err != nil {
		return fmt.Errorf("checking if running with root/Administrator privileges: %w", err)
	}
	if hasRoot && !fromInstall {
		binPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("error while getting executable path: %w", err)
		}
		isOwner, err := isOwnerExec(binPath)
		if err != nil {
			return fmt.Errorf("ran into an error while figuring out if user is allowed to execute the enroll command: %w", err)
		}
		if !isOwner {
			return UserOwnerMismatchError
		}
	}

	pathConfigFile := paths.ConfigFile()
	rawConfig, err := config.LoadFile(pathConfigFile)
	if err != nil {
		return errors.New(err,
			fmt.Sprintf("could not read configuration file %s", pathConfigFile),
			errors.TypeFilesystem,
			errors.M(errors.MetaKeyPath, pathConfigFile))
	}

	cfg, err := configuration.NewFromConfig(rawConfig)
	if err != nil {
		return errors.New(err,
			fmt.Sprintf("could not parse configuration file %s", pathConfigFile),
			errors.TypeFilesystem,
			errors.M(errors.MetaKeyPath, pathConfigFile))
	}

	staging, _ := cmd.Flags().GetString("staging")
	if staging != "" {
		if len(staging) < 8 {
			return errors.New(fmt.Errorf("invalid staging build hash; must be at least 8 characters"), "Error")
		}
	}

	force, _ := cmd.Flags().GetBool("force")
	if fromInstall {
		force = true
	}

	// prompt only when it is not forced and is already enrolled
	if !force && (cfg.Fleet != nil && cfg.Fleet.Enabled) {
		confirm, err := cli.Confirm("This will replace your current settings. Do you want to continue?", true)
		if err != nil {
			return errors.New(err, "problem reading prompt response")
		}
		if !confirm {
			fmt.Fprintln(streams.Out, "Enrollment was cancelled by the user")
			return nil
		}
	}

	// enroll is invoked either manually or from install with redirected IO
	// no need to log to file
	cfg.Settings.LoggingConfig.ToFiles = false
	cfg.Settings.LoggingConfig.ToStderr = true

	logger, err := logger.NewFromConfig("", cfg.Settings.LoggingConfig, cfg.Settings.EventLoggingConfig, false)
	if err != nil {
		return err
	}

	insecure, _ := cmd.Flags().GetBool("insecure")
	url, _ := cmd.Flags().GetString("url")
	enrollmentToken, _ := cmd.Flags().GetString("enrollment-token")
	id, _ := cmd.Flags().GetString("id")
	replaceToken, _ := cmd.Flags().GetString("replace-token")
	fServer, _ := cmd.Flags().GetString("fleet-server-es")
	fElasticSearchCA, _ := cmd.Flags().GetString("fleet-server-es-ca")
	fElasticSearchCASHA256, _ := cmd.Flags().GetString("fleet-server-es-ca-trusted-fingerprint")
	fElasticSearchInsecure, _ := cmd.Flags().GetBool("fleet-server-es-insecure")
	fElasticSearchClientCert, _ := cmd.Flags().GetString("fleet-server-es-cert")
	fElasticSearchClientCertKey, _ := cmd.Flags().GetString("fleet-server-es-cert-key")
	fHeaders, _ := cmd.Flags().GetStringSlice("header")
	fServiceToken, _ := cmd.Flags().GetString("fleet-server-service-token")
	fServiceTokenPath, _ := cmd.Flags().GetString("fleet-server-service-token-path")
	fPolicy, _ := cmd.Flags().GetString("fleet-server-policy")
	fHost, _ := cmd.Flags().GetString("fleet-server-host")
	fPort, _ := cmd.Flags().GetUint16("fleet-server-port")
	fInternalPort, _ := cmd.Flags().GetUint16("fleet-server-internal-port")
	fCert, _ := cmd.Flags().GetString("fleet-server-cert")
	fCertKey, _ := cmd.Flags().GetString("fleet-server-cert-key")
	fPassphrase, _ := cmd.Flags().GetString("fleet-server-cert-key-passphrase")
	fClientAuth, _ := cmd.Flags().GetString("fleet-server-client-auth")
	fInsecure, _ := cmd.Flags().GetBool("fleet-server-insecure-http")
	proxyURL, _ := cmd.Flags().GetString("proxy-url")
	proxyDisabled, _ := cmd.Flags().GetBool("proxy-disabled")
	proxyHeaders, _ := cmd.Flags().GetStringSlice("proxy-header")
	delayEnroll, _ := cmd.Flags().GetBool("delay-enroll")
	daemonTimeout, _ := cmd.Flags().GetDuration("daemon-timeout")
	enrollTimeout, _ := cmd.Flags().GetDuration("enroll-timeout")
	fTimeout, _ := cmd.Flags().GetDuration("fleet-server-timeout")
	skipDaemonReload, _ := cmd.Flags().GetBool("skip-daemon-reload")
	tags, _ := cmd.Flags().GetStringSlice("tag")

	caStr, _ := cmd.Flags().GetString("certificate-authorities")
	CAs := cli.StringToSlice(caStr)
	caSHA256str, _ := cmd.Flags().GetString("ca-sha256")
	caSHA256 := cli.StringToSlice(caSHA256str)
	cert, _ := cmd.Flags().GetString("elastic-agent-cert")
	key, _ := cmd.Flags().GetString("elastic-agent-cert-key")
	keyPassphrase, _ := cmd.Flags().GetString("elastic-agent-cert-key-passphrase")

	ctx := handleSignal(context.Background())

	if enrollTimeout > 0 {
		eCtx, cancel := context.WithTimeout(ctx, enrollTimeout)
		defer cancel()
		ctx = eCtx
	}

	// On MacOS Ventura and above, fixing the permissions on enrollment during installation fails with the error:
	//  Error: failed to fix permissions: chown /Library/Elastic/Agent/data/elastic-agent-c13f91/elastic-agent.app: operation not permitted
	// This is because we are fixing permissions twice, once during installation and again during the enrollment step.
	// When we are enrolling as part of installation on MacOS, skip the second attempt to fix permissions.
	var fixPermissions *utils.FileOwner
	if fromInstall {
		perms, err := getFileOwnerFromCmd(cmd)
		if err != nil {
			// no context is added because the error is clear and user facing
			return err
		}
		fixPermissions = &perms
	}
	if runtime.GOOS == "darwin" {
		fixPermissions = nil
	}

	options := enroll.EnrollOptions{
		EnrollAPIKey:         enrollmentToken,
		ID:                   id,
		ReplaceToken:         replaceToken,
		URL:                  url,
		CAs:                  CAs,
		CASha256:             caSHA256,
		Certificate:          cert,
		Key:                  key,
		KeyPassphrasePath:    keyPassphrase,
		Insecure:             insecure,
		UserProvidedMetadata: make(map[string]interface{}),
		Staging:              staging,
		FixPermissions:       fixPermissions,
		Headers:              mapFromEnvList(fHeaders),
		ProxyURL:             proxyURL,
		ProxyDisabled:        proxyDisabled,
		ProxyHeaders:         mapFromEnvList(proxyHeaders),
		DelayEnroll:          delayEnroll,
		DaemonTimeout:        daemonTimeout,
		SkipDaemonRestart:    skipDaemonReload,
		Tags:                 tags,
		FleetServer: enroll.EnrollCmdFleetServerOption{
			ConnStr:               fServer,
			ElasticsearchCA:       fElasticSearchCA,
			ElasticsearchCASHA256: fElasticSearchCASHA256,
			ElasticsearchInsecure: fElasticSearchInsecure,
			ElasticsearchCert:     fElasticSearchClientCert,
			ElasticsearchCertKey:  fElasticSearchClientCertKey,
			ServiceToken:          fServiceToken,
			ServiceTokenPath:      fServiceTokenPath,
			PolicyID:              fPolicy,
			Host:                  fHost,
			Port:                  fPort,
			Cert:                  fCert,
			CertKey:               fCertKey,
			CertKeyPassphrasePath: fPassphrase,
			ClientAuth:            fClientAuth,
			Insecure:              fInsecure,
			SpawnAgent:            !fromInstall,
			Headers:               mapFromEnvList(fHeaders),
			Timeout:               fTimeout,
			InternalPort:          fInternalPort,
		},
	}

	var storeOpts []storage.ReplaceOnSuccessStoreOptionFunc
	var encryptOpts []storage.EncryptedOptionFunc
	if fixPermissions != nil {
		storeOpts = append(storeOpts, storage.ReplaceOnSuccessStoreWithOwnership(*fixPermissions))
		encryptOpts = append(encryptOpts, storage.EncryptedStoreWithOwnership(*fixPermissions))
	}
	encStore, err := storage.NewEncryptedDiskStore(ctx, paths.AgentConfigFile(), encryptOpts...)
	if err != nil {
		return fmt.Errorf("failed to create encrypted disk store: %w", err)
	}
	store := storage.NewReplaceOnSuccessStore(
		pathConfigFile,
		info.DefaultAgentFleetConfig,
		encStore,
		storeOpts...,
	)

	c, err := newEnrollCmd(
		logger,
		&options,
		pathConfigFile,
		store,
		nil,
	)
	if err != nil {
		return err
	}

	return c.Execute(ctx, streams)
}

func handleSignal(ctx context.Context) context.Context {
	ctx, cfunc := context.WithCancel(ctx)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	go func() {
		select {
		case <-sigs:
			cfunc()
		case <-ctx.Done():
		}

		signal.Stop(sigs)
		close(sigs)
	}()

	return ctx
}

func mapFromEnvList(envList []string) map[string]string {
	m := make(map[string]string)
	for _, kv := range envList {
		keyValue := strings.SplitN(kv, "=", 2)
		if len(keyValue) != 2 {
			continue
		}

		m[keyValue[0]] = keyValue[1]
	}
	return m
}
