package steamcmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/example/conan-manager/internal/server"
)

const (
	linuxDownloadURL = "https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz"
	conanAppID       = "443030"
)

type Service struct {
	HTTPClient *http.Client
	Output     io.Writer
}

func (s Service) Ensure(ctx context.Context, directory string) (string, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return "", fmt.Errorf("steamcmd directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", err
	}
	binary := filepath.Join(directory, "steamcmd.sh")
	if _, err := os.Stat(binary); err == nil {
		return binary, nil
	}
	client := s.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Get(linuxDownloadURL)
	if err != nil {
		return "", fmt.Errorf("download SteamCMD: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download SteamCMD: HTTP %s", response.Status)
	}
	gz, err := gzip.NewReader(response.Body)
	if err != nil {
		return "", fmt.Errorf("read SteamCMD archive: %w", err)
	}
	defer gz.Close()
	archive := tar.NewReader(gz)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		name := filepath.Clean(header.Name)
		if name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("unsafe SteamCMD archive path")
		}
		target := filepath.Join(directory, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return "", err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o750)
			if err != nil {
				return "", err
			}
			_, copyErr := io.Copy(file, archive)
			closeErr := file.Close()
			if copyErr != nil {
				return "", copyErr
			}
			if closeErr != nil {
				return "", closeErr
			}
		}
	}
	return binary, nil
}

func (s Service) InstallServer(ctx context.Context, cfg server.Config) error {
	binary, err := s.Ensure(ctx, cfg.SteamCMDDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.InstallDir, 0o750); err != nil {
		return err
	}
	args := []string{"+force_install_dir", cfg.InstallDir, "+login", "anonymous", "+app_update", conanAppID}
	if cfg.Branch != "" && cfg.Branch != "public" {
		args = append(args, "-beta", cfg.Branch)
	}
	args = append(args, "+quit")
	if err := runUpdate(ctx, binary, cfg.SteamCMDDir, args, s.Output); err != nil {
		return fmt.Errorf("SteamCMD install: %w", err)
	}
	return nil
}
