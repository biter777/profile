// Package profile provides a simple way to manage runtime/pprof
// profiling of your Go application. Multi profiling supported.
package profile

import (
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync/atomic"
)

// Profile represents an active profiling session.
type Profile struct {
	// quiet suppresses informational messages during profiling.
	quiet bool

	// noShutdownHook controls whether the profiling package should
	// hook SIGINT to write profiles cleanly.
	noShutdownHook bool

	// cpuMode holds the CPU type of profiling
	cpuMode bool
	// memMode holds the memory type of profiling
	memMode bool
	// mutexMode holds the mutex type of profiling
	mutexMode bool
	// blockMode holds the block type of profiling
	blockMode bool
	// traceMode make trace out
	traceMode bool

	// path holds the base path where various profiling files are  written.
	// If blank, the base path will be generated by ioutil.TempDir.
	path string

	// memProfileRate holds the rate for the memory profile.
	memProfileRate int

	// closers holds a cleanup functions that run after each profile
	closers []func()

	// stopped records if a call to profile.Stop has been made
	stopped uint32
}

// NoShutdownHook controls whether the profiling package should
// hook SIGINT to write profiles cleanly.
// Programs with more sophisticated signal handling should set
// this to true and ensure the Stop() function returned from Start()
// is called during shutdown.
func NoShutdownHook(p *Profile) { p.noShutdownHook = true }

// Quiet suppresses informational messages during profiling.
func Quiet(p *Profile) { p.quiet = true }

// CPUProfile enables cpu profiling.
// It NOT disables any previous profiling settings (multi profiling supported).
func CPUProfile(p *Profile) { p.cpuMode = true }

// DefaultMemProfileRate is the default memory profiling rate.
// See also http://golang.org/pkg/runtime/#pkg-variables
const DefaultMemProfileRate = 4096

// MemProfile enables memory profiling.
// It NOT disables any previous profiling settings (multi profiling supported).
func MemProfile(p *Profile) {
	p.memProfileRate = DefaultMemProfileRate
	p.memMode = true
}

// MemProfileRate enables memory profiling at the preferred rate.
// It NOT disables any previous profiling settings (multi profiling supported).
func MemProfileRate(rate int) func(*Profile) {
	return func(p *Profile) {
		p.memProfileRate = rate
		p.memMode = true
	}
}

// MutexProfile enables mutex profiling.
// It NOT disables any previous profiling settings (multi profiling supported).
//
// Mutex profiling is a no-op before go1.8.
func MutexProfile(p *Profile) { p.mutexMode = true }

// BlockProfile enables block (contention) profiling.
// It NOT disables any previous profiling settings (multi profiling supported).
func BlockProfile(p *Profile) { p.blockMode = true }

// TraceProfile profile controls if execution tracing will be enabled.
// It NOT disables any previous profiling settings (multi profiling supported).
func TraceProfile(p *Profile) { p.traceMode = true }

// ProfileAll set to enables CPUProfile, MemProfile, MutexProfile, BlockProfile and TraceProfile.
// Multi profiling supported.
func ProfileAll(p *Profile) {
	p.cpuMode = true
	p.memMode = true
	p.mutexMode = true
	p.blockMode = true
	p.traceMode = true
}

// ProfilePath controls the base path where various profiling
// files are written. If blank, the base path will be generated
// by ioutil.TempDir.
func ProfilePath(path string) func(*Profile) {
	return func(p *Profile) {
		p.path = path
	}
}

// ProfilePathLocalDir setup the base path where various profiling
// files are written to: .../LocalDir/profile12345. (12345 - auto generated)
func ProfilePathLocalDir(p *Profile) {
	p.path, _ = ioutil.TempDir(localDir(), "profile")
}

// Stop stops the profile and flushes any unwritten data.
func (p *Profile) Stop() {
	if !atomic.CompareAndSwapUint32(&p.stopped, 0, 1) {
		// someone has already called close
		return
	}

	for _, closer := range p.closers {
		closer()
	}
	atomic.StoreUint32(&started, 0)
}

// started is non zero if a profile is running.
var started uint32

// Start starts a new profiling session.
// The caller should call the Stop method on the value returned
// to cleanly stop profiling.
func Start(options ...func(*Profile)) interface {
	Stop()
} {
	if !atomic.CompareAndSwapUint32(&started, 0, 1) {
		log.Fatal("profile: Start() already called")
	}

	var prof Profile
	for _, option := range options {
		option(&prof)
	}
	if !prof.cpuMode && !prof.memMode && !prof.mutexMode && !prof.blockMode && !prof.traceMode {
		ProfileAll(&prof) // Default
	}

	path, err := func() (string, error) {
		if p := prof.path; p != "" {
			return p, os.MkdirAll(p, 0777)
		}
		return ioutil.TempDir("", "profile")
	}()

	if err != nil {
		log.Fatalf("profile: could not create initial output directory: %v", err)
	}

	logf := func(format string, args ...interface{}) {
		if !prof.quiet {
			log.Printf(format, args...)
		}
	}

	if prof.cpuMode {
		fn := filepath.Join(path, "cpu.pprof")
		f, err := os.Create(fn)
		if err != nil {
			log.Fatalf("profile: could not create cpu profile %q: %v", fn, err)
		}
		logf("profile: cpu profiling enabled, %s", fn)
		pprof.StartCPUProfile(f)
		prof.closers = append(prof.closers,
			func() {
				pprof.StopCPUProfile()
				f.Close()
				logf("profile: cpu profiling disabled, %s", fn)
			})
	}

	if prof.memMode {
		fn := filepath.Join(path, "mem.pprof")
		f, err := os.Create(fn)
		if err != nil {
			log.Fatalf("profile: could not create memory profile %q: %v", fn, err)
		}
		old := runtime.MemProfileRate
		runtime.MemProfileRate = prof.memProfileRate
		logf("profile: memory profiling enabled (rate %d), %s", runtime.MemProfileRate, fn)
		prof.closers = append(prof.closers,
			func() {
				pprof.Lookup("heap").WriteTo(f, 0)
				f.Close()
				runtime.MemProfileRate = old
				logf("profile: memory profiling disabled, %s", fn)
			})
	}

	if prof.mutexMode {
		fn := filepath.Join(path, "mutex.pprof")
		f, err := os.Create(fn)
		if err != nil {
			log.Fatalf("profile: could not create mutex profile %q: %v", fn, err)
		}
		enableMutexProfile()
		logf("profile: mutex profiling enabled, %s", fn)
		prof.closers = append(prof.closers,
			func() {
				if mp := pprof.Lookup("mutex"); mp != nil {
					mp.WriteTo(f, 0)
				}
				f.Close()
				disableMutexProfile()
				logf("profile: mutex profiling disabled, %s", fn)
			})
	}

	if prof.blockMode {
		fn := filepath.Join(path, "block.pprof")
		f, err := os.Create(fn)
		if err != nil {
			log.Fatalf("profile: could not create block profile %q: %v", fn, err)
		}
		runtime.SetBlockProfileRate(1)
		logf("profile: block profiling enabled, %s", fn)
		prof.closers = append(prof.closers,
			func() {
				pprof.Lookup("block").WriteTo(f, 0)
				f.Close()
				runtime.SetBlockProfileRate(0)
				logf("profile: block profiling disabled, %s", fn)
			})
	}

	if prof.traceMode {
		fn := filepath.Join(path, "trace.out")
		f, err := os.Create(fn)
		if err != nil {
			log.Fatalf("profile: could not create trace output file %q: %v", fn, err)
		}
		if err := startTrace(f); err != nil {
			log.Fatalf("profile: could not start trace: %v", err)
		}
		logf("profile: trace enabled, %s", fn)
		prof.closers = append(prof.closers,
			func() {
				stopTrace()
				logf("profile: trace disabled, %s", fn)
			})
	}

	if !prof.noShutdownHook {
		go func() {
			log.Println("profile: set interrupt catcher")
			c := make(chan os.Signal, 1)
			signal.Notify(c, os.Interrupt)
			<-c

			log.Println("profile: caught interrupt, stopping profiles")
			prof.Stop()

			os.Exit(0)
		}()
	}

	return &prof
}

func localDir() string {
	ex, err := os.Executable()
	if err != nil {
		exReal, err := filepath.EvalSymlinks(ex)
		if err != nil {
			return ""
		}
		return filepath.Dir(exReal)
	}
	return filepath.Dir(ex)
}
