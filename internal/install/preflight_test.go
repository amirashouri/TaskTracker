package install

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fakePreflightEnv struct {
	lookPathErr error
	dockerRoot  string
	dockerErr   error
	listenErr   error
	ensureErr   error

	listenCalls    []string
	ensurePaths    []string
	availableCalls []string
	available      map[string]uint64
	availableErr   map[string]error
}

func (f *fakePreflightEnv) LookPath(name string) (string, error) {
	if f.lookPathErr != nil {
		return "", f.lookPathErr
	}
	return "/usr/bin/" + name, nil
}

func (f *fakePreflightEnv) DockerRoot(context.Context) (string, error) {
	return f.dockerRoot, f.dockerErr
}

func (f *fakePreflightEnv) Listen(_ string, address string) (io.Closer, error) {
	f.listenCalls = append(f.listenCalls, address)
	if f.listenErr != nil {
		return nil, f.listenErr
	}
	return fakeCloser{}, nil
}

func (f *fakePreflightEnv) EnsureWritable(path string) error {
	f.ensurePaths = append(f.ensurePaths, path)
	return f.ensureErr
}

func (f *fakePreflightEnv) AvailableBytes(path string) (uint64, error) {
	f.availableCalls = append(f.availableCalls, path)
	if err, ok := f.availableErr[path]; ok {
		return 0, err
	}
	if bytes, ok := f.available[path]; ok {
		return bytes, nil
	}
	return 1 << 40, nil
}

type fakeCloser struct{}

func (fakeCloser) Close() error { return nil }

func testPreflightOptions() PreflightOptions {
	return PreflightOptions{
		DataDir:  "/data",
		Mode:     ModeSQLite,
		Port:     8080,
		Platform: "linux",
	}
}

func TestPreflightPasses(t *testing.T) {
	env := &fakePreflightEnv{
		dockerRoot: "/var/lib/docker",
		available:  map[string]uint64{"/var/lib/docker": 2 << 30},
	}

	if err := preflight(context.Background(), env, testPreflightOptions()); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if len(env.ensurePaths) != 1 || env.ensurePaths[0] != "/data" {
		t.Errorf("writability checks = %v, want /data", env.ensurePaths)
	}
	if len(env.listenCalls) != 1 || env.listenCalls[0] != "127.0.0.1:8080" {
		t.Errorf("port checks = %v, want 127.0.0.1:8080", env.listenCalls)
	}
	if len(env.availableCalls) == 0 || env.availableCalls[0] != "/var/lib/docker" {
		t.Errorf("disk checks = %v, want the Linux Docker root first", env.availableCalls)
	}
}

func TestPreflightMissingDockerNamesTheInstallURL(t *testing.T) {
	cases := []struct {
		platform string
		want     string
	}{
		{platform: "linux", want: "Docker Engine"},
		{platform: "darwin", want: "Docker Desktop"},
		{platform: "windows", want: "Docker Desktop"},
	}
	for _, tc := range cases {
		t.Run(tc.platform, func(t *testing.T) {
			opts := testPreflightOptions()
			opts.Platform = tc.platform
			env := &fakePreflightEnv{lookPathErr: errors.New("not found")}

			err := preflight(context.Background(), env, opts)
			if err == nil {
				t.Fatal("preflight accepted a host without Docker")
			}
			if !strings.Contains(err.Error(), "not installed") || !strings.Contains(err.Error(), "docs.docker.com") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want the %s install URL", err, tc.want)
			}
			if len(env.ensurePaths) != 0 || len(env.listenCalls) != 0 {
				t.Error("preflight continued after Docker was missing")
			}
		})
	}
}

func TestPreflightUnreachableDaemonNamesTheStartHint(t *testing.T) {
	opts := testPreflightOptions()
	opts.Platform = "darwin"
	env := &fakePreflightEnv{dockerErr: errors.New("cannot connect to the daemon")}

	err := preflight(context.Background(), env, opts)
	if err == nil {
		t.Fatal("preflight accepted an unreachable Docker daemon")
	}
	if !strings.Contains(err.Error(), "daemon is not reachable") || !strings.Contains(err.Error(), "Start Docker Desktop") {
		t.Errorf("error = %q, want a daemon explanation and a start hint", err)
	}
	if len(env.ensurePaths) != 0 {
		t.Error("preflight checked the data directory after the daemon failed")
	}
}

func TestPreflightUnwritableDataDirNamesTheFlag(t *testing.T) {
	env := &fakePreflightEnv{
		dockerRoot: "/var/lib/docker",
		ensureErr:  errors.New("permission denied"),
	}

	err := preflight(context.Background(), env, testPreflightOptions())
	if err == nil {
		t.Fatal("preflight accepted an unwritable data directory")
	}
	if !strings.Contains(err.Error(), "not writable") || !strings.Contains(err.Error(), "--data-dir") {
		t.Errorf("error = %q, want the path and the --data-dir fix", err)
	}
	if len(env.listenCalls) != 0 {
		t.Error("preflight checked the port after the data directory failed")
	}
}

func TestPreflightBusyPortNamesTheFlag(t *testing.T) {
	env := &fakePreflightEnv{
		dockerRoot: "/var/lib/docker",
		listenErr:  errors.New("address already in use"),
	}

	err := preflight(context.Background(), env, testPreflightOptions())
	if err == nil {
		t.Fatal("preflight accepted a busy port")
	}
	if !strings.Contains(err.Error(), "already in use") || !strings.Contains(err.Error(), "--port") {
		t.Errorf("error = %q, want the port and the --port fix", err)
	}
}

func TestPreflightRepairSkipsThePortCheck(t *testing.T) {
	env := &fakePreflightEnv{
		dockerRoot: "/var/lib/docker",
		listenErr:  errors.New("address already in use"),
	}
	opts := testPreflightOptions()
	opts.Repair = true

	if err := preflight(context.Background(), env, opts); err != nil {
		t.Fatalf("repair preflight: %v", err)
	}
	if len(env.listenCalls) != 0 {
		t.Errorf("repair checked the port: %v", env.listenCalls)
	}
}

func TestPreflightLowDiskNamesTheAmountAndPath(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		env := &fakePreflightEnv{
			dockerRoot: "/var/lib/docker",
			available:  map[string]uint64{"/var/lib/docker": 100 << 20},
		}

		err := preflight(context.Background(), env, testPreflightOptions())
		if err == nil {
			t.Fatal("preflight accepted a nearly full disk")
		}
		if !strings.Contains(err.Error(), "not enough free space") || !strings.Contains(err.Error(), "512.0 MiB") {
			t.Errorf("error = %q, want the sqlite minimum", err)
		}
		if !strings.Contains(err.Error(), "/var/lib/docker") {
			t.Errorf("error = %q, want the checked path", err)
		}
	})

	t.Run("postgres", func(t *testing.T) {
		env := &fakePreflightEnv{
			dockerRoot: "/var/lib/docker",
			available:  map[string]uint64{"/var/lib/docker": 1 << 30},
		}
		opts := testPreflightOptions()
		opts.Mode = ModePostgres

		err := preflight(context.Background(), env, opts)
		if err == nil {
			t.Fatal("preflight accepted a Postgres install without enough space")
		}
		if !strings.Contains(err.Error(), "2.0 GiB") {
			t.Errorf("error = %q, want the postgres minimum", err)
		}
	})
}

func TestPreflightUsesTheDataDirProxyOutsideLinux(t *testing.T) {
	env := &fakePreflightEnv{
		dockerRoot: "/var/lib/docker",
		available: map[string]uint64{
			"/var/lib/docker": 1 << 20,
			"/data":           2 << 30,
		},
	}
	opts := testPreflightOptions()
	opts.Platform = "darwin"

	if err := preflight(context.Background(), env, opts); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if len(env.availableCalls) != 1 || env.availableCalls[0] != "/data" {
		t.Errorf("disk checks = %v, want the data directory on macOS", env.availableCalls)
	}
}

func TestPreflightFallsBackWhenTheDockerRootIsUnreadable(t *testing.T) {
	env := &fakePreflightEnv{
		dockerRoot:   "/var/lib/docker",
		available:    map[string]uint64{"/data": 2 << 30},
		availableErr: map[string]error{"/var/lib/docker": errors.New("permission denied")},
	}

	if err := preflight(context.Background(), env, testPreflightOptions()); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if len(env.availableCalls) != 2 || env.availableCalls[1] != "/data" {
		t.Errorf("disk checks = %v, want Docker root then data directory", env.availableCalls)
	}
}

func TestPreflightSkipsAnUnknownDiskSize(t *testing.T) {
	env := &fakePreflightEnv{
		dockerRoot: "/var/lib/docker",
		available:  map[string]uint64{"/var/lib/docker": 0},
	}

	if err := preflight(context.Background(), env, testPreflightOptions()); err != nil {
		t.Fatalf("preflight: %v", err)
	}
}

func TestPreflightRejectsInvalidOptions(t *testing.T) {
	cases := []struct {
		name string
		opts PreflightOptions
		want string
	}{
		{name: "empty data dir", opts: PreflightOptions{Mode: ModeSQLite, Port: 8080}, want: "--data-dir"},
		{name: "bad mode", opts: PreflightOptions{DataDir: "/data", Mode: "mysql", Port: 8080}, want: "--mode"},
		{name: "bad port", opts: PreflightOptions{DataDir: "/data", Mode: ModeSQLite, Port: 0}, want: "--port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := &fakePreflightEnv{}
			err := preflight(context.Background(), env, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want a reference to %s", err, tc.want)
			}
		})
	}
}

func TestEnsureWritableCreatesAndCleansUp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := (osPreflightEnv{}).EnsureWritable(dir); err != nil {
		t.Fatalf("EnsureWritable: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat data dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("EnsureWritable did not create a directory")
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".tt-write-*"))
	if err != nil {
		t.Fatalf("glob temporary files: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("EnsureWritable left temporary files: %v", matches)
	}
}

func TestAvailableBytesReportsPositiveSpace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reports disk space as unknown by design")
	}
	available, err := availableBytes(t.TempDir())
	if err != nil {
		t.Fatalf("availableBytes: %v", err)
	}
	if available == 0 {
		t.Error("availableBytes returned zero for a real directory")
	}
}
