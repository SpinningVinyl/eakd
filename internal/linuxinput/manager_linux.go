//go:build linux

package linuxinput

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"eak/internal/input"
)

const (
	candidateCheckInterval = 20 * time.Millisecond
	acquisitionRetryDelay  = 100 * time.Millisecond
	maxReacquisitionTries  = 5
)

var errEventNodeChanged = errors.New("event device node changed while being opened")

type acquisitionRetry struct {
	generation deviceGeneration
	attempts   int
	deadline   time.Time
	scheduled  bool
}

func (r *acquisitionRetry) begin() bool {
	if r.attempts >= maxReacquisitionTries {
		return false
	}
	r.attempts++
	r.scheduled = false
	return true
}

type physicalDevice struct {
	path       string
	generation deviceGeneration
	fd         int
	frame      []input.Event
	dropping   bool
	leds       [3]bool
}

type Manager struct {
	virtualName string
	logger      *log.Logger
	ready       chan struct{}
	locks       LockState
	ledMu       sync.Mutex
	ledPending  [3]bool
	ledDirty    [3]bool
}

type LEDUpdate struct {
	Code    uint16
	Enabled bool
}

func NewManager(virtualName string, logger *log.Logger) *Manager {
	return &Manager{virtualName: virtualName, logger: logger, ready: make(chan struct{})}
}

// Ready is closed after the udev monitor is active and initial enumeration has
// finished. A keyboard with held keys may still be waiting for an idle handoff.
func (m *Manager) Ready() <-chan struct{} { return m.ready }

// SetLEDs records one compositor feedback batch without blocking the virtual
// keyboard reader. Updates are coalesced and applied by the epoll owner.
func (m *Manager) SetLEDs(updates []LEDUpdate) {
	m.ledMu.Lock()
	for _, update := range updates {
		if update.Code > input.LEDScrollLock {
			continue
		}
		m.ledPending[update.Code] = update.Enabled
		m.ledDirty[update.Code] = true
	}
	m.ledMu.Unlock()
}

// Run owns every physical descriptor, the udev monitor, and the epoll instance.
// A single epoll loop preserves ordering without one goroutine per keyboard.
func (m *Manager) Run(ctx context.Context, output chan<- input.Message) {
	defer close(output)
	epfd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		_ = sendMessage(ctx, output, input.Message{Err: fmt.Errorf("epoll_create1: %w", err)})
		return
	}
	defer syscall.Close(epfd)

	monitorFD, err := openUdevMonitor()
	if err != nil {
		_ = sendMessage(ctx, output, input.Message{Err: err})
		return
	}
	defer syscall.Close(monitorFD)
	if err := addEpollFD(epfd, monitorFD); err != nil {
		_ = sendMessage(ctx, output, input.Message{Err: fmt.Errorf("epoll add udev monitor: %w", err)})
		return
	}
	byPath := make(map[string]*physicalDevice)
	byFD := make(map[int]*physicalDevice)
	candidates := make(map[string]*physicalDevice)
	retries := make(map[string]acquisitionRetry)
	latestSequence := make(map[string]uint64)
	defer func() {
		for _, device := range byPath {
			m.closeDevice(epfd, device)
		}
		for _, device := range candidates {
			m.closeCandidate(device)
		}
	}()

	syncLEDs := func() {
		for _, device := range byPath {
			if err := m.syncDeviceLEDs(device); err != nil {
				m.logger.Printf("synchronize LEDs on %s: %v", device.path, err)
			}
		}
	}
	scheduleRetry := func(path string, generation deviceGeneration) {
		state := retries[path]
		if state.generation != generation {
			state = acquisitionRetry{generation: generation}
		}
		if state.attempts >= maxReacquisitionTries {
			state.scheduled = false
			retries[path] = state
			m.logger.Printf("giving up reacquiring %s after %d attempts", path, state.attempts)
			return
		}
		state.deadline = time.Now().Add(acquisitionRetryDelay)
		state.scheduled = true
		retries[path] = state
	}
	beginRetry := func(path string, generation deviceGeneration) bool {
		state, tracked := retries[path]
		if !tracked {
			return true
		}
		if state.generation != generation {
			state = acquisitionRetry{generation: generation}
		}
		if !state.begin() {
			return false
		}
		retries[path] = state
		return true
	}
	activate := func(device *physicalDevice) error {
		pressed, acquireErr := m.acquireIdleDevice(epfd, device)
		if acquireErr != nil {
			return acquireErr
		}
		if pressed == nil {
			return nil
		}
		delete(candidates, device.path)
		delete(retries, device.path)
		byPath[device.path] = device
		byFD[device.fd] = device
		if err := m.syncDeviceLEDs(device); err != nil {
			m.logger.Printf("initialize LEDs on %s: %v", device.path, err)
		}
		m.logger.Printf("grabbed idle keyboard %s", device.path)
		return sendMessage(ctx, output, input.Message{
			Resync: &input.Resync{Device: device.path, Pressed: pressed},
		})
	}
	acceptCandidate := func(device *physicalDevice) error {
		path := device.path
		generation := device.generation
		candidates[path] = device
		pressed, stateErr := m.initialKeysOrClose(device)
		if stateErr != nil {
			delete(candidates, path)
			m.logger.Printf("query initial state for %s; closed for retry: %v", path, stateErr)
			scheduleRetry(path, generation)
			return nil
		}
		if len(pressed) != 0 {
			m.logger.Printf("keyboard %s has held keys; waiting for idle state before EVIOCGRAB", path)
			return nil
		}
		if err := activate(device); err != nil {
			delete(candidates, path)
			m.closeCandidate(device)
			m.logger.Printf("acquire %s; closed for retry: %v", path, err)
			scheduleRetry(path, generation)
			return nil
		}
		return nil
	}
	consider := func(path string, generation deviceGeneration) error {
		if _, exists := byPath[path]; exists {
			return nil
		}
		if _, exists := candidates[path]; exists {
			return nil
		}
		device, openErr := m.openCandidate(path, generation)
		if openErr != nil {
			if errors.Is(openErr, errEventNodeChanged) {
				delete(retries, path)
				m.logger.Printf("ignore stale generation for %s: %v", path, openErr)
				return nil
			}
			if !errors.Is(openErr, syscall.EACCES) && !errors.Is(openErr, syscall.EPERM) {
				m.logger.Printf("defer %s: %v", path, openErr)
			}
			scheduleRetry(path, generation)
			return nil
		}
		if device == nil {
			delete(retries, path)
			return nil
		}
		return acceptCandidate(device)
	}
	handleAdd := func(event deviceEvent) error {
		generation := generationFromEvent(event)
		if state, exists := retries[event.path]; exists && state.generation.devPath == generation.devPath && !state.generation.observedAdd {
			state.generation = adoptObservedAdd(state.generation, generation)
			generation = state.generation
			retries[event.path] = state
		}
		if device := byPath[event.path]; device != nil && device.generation.devPath == generation.devPath && !device.generation.observedAdd {
			device.generation = adoptObservedAdd(device.generation, generation)
			return nil
		}
		if device := candidates[event.path]; device != nil && device.generation.devPath == generation.devPath && !device.generation.observedAdd {
			device.generation = adoptObservedAdd(device.generation, generation)
			if state, exists := retries[event.path]; exists {
				state.generation = device.generation
				retries[event.path] = state
			}
			return nil
		}
		if device := byPath[event.path]; device != nil {
			m.closeDevice(epfd, device)
			delete(byPath, event.path)
			delete(byFD, device.fd)
			if err := sendMessage(ctx, output, input.Message{Removed: event.path}); err != nil {
				return err
			}
		}
		if device := candidates[event.path]; device != nil {
			m.closeCandidate(device)
			delete(candidates, event.path)
		}
		if _, exists := byPath[event.path]; exists {
			return nil
		}
		if _, exists := candidates[event.path]; exists {
			return nil
		}
		if !beginRetry(event.path, generation) {
			return nil
		}
		return consider(event.path, generation)
	}
	remove := func(event deviceEvent) error {
		if state, exists := retries[event.path]; exists && generationMatchesRemoval(state.generation, event) {
			delete(retries, event.path)
		}
		if device := candidates[event.path]; device != nil && generationMatchesRemoval(device.generation, event) {
			delete(candidates, event.path)
			m.closeCandidate(device)
		}
		device := byPath[event.path]
		if device == nil || !generationMatchesRemoval(device.generation, event) {
			return nil
		}
		m.closeDevice(epfd, device)
		delete(byPath, event.path)
		delete(byFD, device.fd)
		return sendMessage(ctx, output, input.Message{Removed: event.path})
	}

	// The monitor is subscribed before enumeration, so additions racing with
	// the glob remain queued on the netlink descriptor.
	paths, globErr := filepath.Glob("/dev/input/event*")
	if globErr != nil {
		_ = sendMessage(ctx, output, input.Message{Err: fmt.Errorf("enumerate input devices: %w", globErr)})
		return
	}
	for _, path := range paths {
		device, generation, openErr := m.openEnumeratedCandidate(path)
		if openErr != nil {
			if errors.Is(openErr, errKernelUeventSequence) {
				_ = sendMessage(ctx, output, input.Message{
					Err: fmt.Errorf("establish uevent ordering for initial device %s: %w", path, openErr),
				})
				return
			}
			if errors.Is(openErr, errEventNodeChanged) {
				m.logger.Printf("skip changed initial device %s; queued udev events will identify its replacement", path)
				continue
			}
			m.logger.Printf("defer initial device %s: %v", path, openErr)
			if generation.devPath != "" {
				scheduleRetry(path, generation)
			}
			continue
		}
		if device == nil {
			continue
		}
		if err := acceptCandidate(device); err != nil {
			return
		}
	}
	close(m.ready)
	events := make([]syscall.EpollEvent, 32)
	readBuffer := make([]byte, kernelEventSize*eventBufferSize)
	udevBuffer := make([]byte, udevBufferSize)
	nextCandidateCheck := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if m.applyPendingLEDs() {
			caps, num, scroll := m.locks.Snapshot()
			m.logger.Printf("compositor lock state caps=%t num=%t scroll=%t", caps, num, scroll)
			syncLEDs()
		}
		now := time.Now()
		if !now.Before(nextCandidateCheck) {
			for path, device := range candidates {
				pressed, stateErr := currentKeys(device.fd)
				if stateErr != nil {
					delete(candidates, path)
					m.closeCandidate(device)
					if !errors.Is(stateErr, syscall.ENODEV) && !errors.Is(stateErr, syscall.ENOENT) {
						m.logger.Printf("query pending state for %s; closed for retry: %v", path, stateErr)
						scheduleRetry(path, device.generation)
					}
					continue
				}
				if len(pressed) != 0 {
					continue
				}
				if err := activate(device); err != nil {
					delete(candidates, path)
					m.closeCandidate(device)
					m.logger.Printf("acquire %s; closed for retry: %v", path, err)
					scheduleRetry(path, device.generation)
				}
			}
			nextCandidateCheck = now.Add(candidateCheckInterval)
		}
		pendingRetry := false
		for path, state := range retries {
			if !state.scheduled {
				continue
			}
			pendingRetry = true
			if !now.Before(state.deadline) {
				if !beginRetry(path, state.generation) {
					continue
				}
				if err := consider(path, state.generation); err != nil {
					return
				}
			}
		}

		timeout := 250
		if len(candidates) != 0 || pendingRetry {
			timeout = int(candidateCheckInterval / time.Millisecond)
		}
		count, waitErr := syscall.EpollWait(epfd, events, timeout)
		if waitErr != nil {
			if waitErr == syscall.EINTR {
				continue
			}
			_ = sendMessage(ctx, output, input.Message{Err: fmt.Errorf("epoll_wait: %w", waitErr)})
			return
		}
		for _, ready := range events[:count] {
			if int(ready.Fd) == monitorFD {
				if ready.Events&(syscall.EPOLLERR|syscall.EPOLLHUP) != 0 {
					_ = sendMessage(ctx, output, input.Message{Err: errors.New("udev monitor closed")})
					return
				}
				deviceEvents, readErr := readDeviceEvents(monitorFD, udevBuffer)
				if readErr != nil {
					_ = sendMessage(ctx, output, input.Message{Err: fmt.Errorf("read udev monitor: %w", readErr)})
					return
				}
				for _, event := range deviceEvents {
					if !acceptEventSequence(latestSequence, event) {
						continue
					}
					if event.action == "remove" {
						if err := remove(event); err != nil {
							return
						}
					} else if err := handleAdd(event); err != nil {
						return
					}
				}
				continue
			}
			device := byFD[int(ready.Fd)]
			if device == nil {
				continue
			}
			removed, readErr := m.readReady(ctx, device, readBuffer, output)
			if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
				return
			}
			if readErr != nil {
				m.logger.Printf("read %s: %v", device.path, readErr)
			}
			if removed || readErr != nil || ready.Events&(syscall.EPOLLHUP|syscall.EPOLLERR) != 0 {
				m.closeDevice(epfd, device)
				delete(byPath, device.path)
				delete(byFD, device.fd)
				if err := sendMessage(ctx, output, input.Message{Removed: device.path}); err != nil {
					return
				}
				if !removed {
					scheduleRetry(device.path, device.generation)
				}
			}
		}
	}
}

func (m *Manager) applyPendingLEDs() bool {
	m.ledMu.Lock()
	pending := m.ledPending
	dirty := m.ledDirty
	m.ledDirty = [3]bool{}
	m.ledMu.Unlock()

	changed := false
	for code := uint16(0); code <= input.LEDScrollLock; code++ {
		if dirty[code] {
			changed = m.locks.SetLED(code, pending[code]) || changed
		}
	}
	return changed
}

func (m *Manager) initialKeysOrClose(device *physicalDevice) (map[uint16]bool, error) {
	pressed, err := currentKeys(device.fd)
	if err != nil {
		m.closeCandidate(device)
		return nil, err
	}
	return pressed, nil
}

func (m *Manager) openCandidate(path string, generation deviceGeneration) (*physicalDevice, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := validateOpenedNode(fd, path, generation.devPath); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return m.probeCandidate(path, generation, fd)
}

// openEnumeratedCandidate establishes which device an event-node descriptor
// refers to before probing it. The sequence number is sampled only after the
// descriptor and its sysfs DEVPATH have been obtained. Comparing fstat(2) on
// the descriptor with stat(2) on the pathname then proves that the pathname
// still names that descriptor's device at the end of the observation window.
func (m *Manager) openEnumeratedCandidate(path string) (*physicalDevice, deviceGeneration, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		generation, generationErr := enumeratedRetryGeneration(path)
		if generationErr != nil {
			return nil, deviceGeneration{}, errors.Join(err, generationErr)
		}
		return nil, generation, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			syscall.Close(fd)
		}
	}()

	var opened syscall.Stat_t
	if err := syscall.Fstat(fd, &opened); err != nil {
		return nil, deviceGeneration{}, fmt.Errorf("fstat opened %s: %w", path, err)
	}
	generation, err := sysfsDeviceGeneration(path)
	if err != nil {
		return nil, deviceGeneration{}, err
	}
	generation.sequenceFloor, err = kernelUeventSequence()
	if err != nil {
		return nil, deviceGeneration{}, err
	}
	if err := validateNodeIdentity(path, opened); err != nil {
		return nil, generation, err
	}

	closeOnError = false
	device, err := m.probeCandidate(path, generation, fd)
	return device, generation, err
}

// enumeratedRetryGeneration provides a bounded retry identity when open(2)
// itself fails and therefore no descriptor exists to bind with fstat(2).
func enumeratedRetryGeneration(path string) (deviceGeneration, error) {
	generation, err := sysfsDeviceGeneration(path)
	if err != nil {
		return deviceGeneration{}, err
	}
	generation.sequenceFloor, err = kernelUeventSequence()
	return generation, err
}

func validateOpenedNode(fd int, path, expectedDevPath string) error {
	var opened syscall.Stat_t
	if err := syscall.Fstat(fd, &opened); err != nil {
		return fmt.Errorf("fstat opened %s: %w", path, err)
	}
	generation, err := sysfsDeviceGeneration(path)
	if err != nil {
		return err
	}
	if generation.devPath != expectedDevPath {
		return fmt.Errorf("%w: expected DEVPATH %s, found %s", errEventNodeChanged, expectedDevPath, generation.devPath)
	}
	return validateNodeIdentity(path, opened)
}

func validateNodeIdentity(path string, opened syscall.Stat_t) error {
	var current syscall.Stat_t
	if err := syscall.Stat(path, &current); err != nil {
		return fmt.Errorf("stat current %s: %w", path, err)
	}
	if !sameDeviceNode(opened, current) {
		return fmt.Errorf("%w: %s no longer names the opened device", errEventNodeChanged, path)
	}
	return nil
}

func sameDeviceNode(left, right syscall.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Rdev == right.Rdev
}

func (m *Manager) probeCandidate(path string, generation deviceGeneration, fd int) (*physicalDevice, error) {
	closeOnError := true
	defer func() {
		if closeOnError {
			syscall.Close(fd)
		}
	}()

	name, err := deviceName(fd)
	if err != nil {
		return nil, fmt.Errorf("EVIOCGNAME: %w", err)
	}
	if name == m.virtualName {
		return nil, nil
	}
	capabilities, err := keyCapabilities(fd)
	isKeyboard, err := classifyKeyboardCapabilities(capabilities, err)
	if err != nil {
		return nil, err
	}
	if !isKeyboard {
		return nil, nil
	}
	ledBits, ledErr := ledCapabilities(fd)
	var leds [3]bool
	if ledErr == nil {
		for code := uint16(0); code <= input.LEDScrollLock; code++ {
			leds[code] = bitIsSet(ledBits, code)
		}
	}
	closeOnError = false
	return &physicalDevice{path: path, generation: generation, fd: fd, leds: leds}, nil
}

func classifyKeyboardCapabilities(capabilities []byte, queryErr error) (bool, error) {
	if queryErr != nil {
		return false, fmt.Errorf("EVIOCGBIT(EV_KEY): %w", queryErr)
	}
	return looksLikeKeyboard(capabilities) || looksLikeNumpad(capabilities), nil
}

// acquireIdleDevice performs the idle check before EVIOCGRAB. The candidate's
// pre-grab event queue is discarded because those events were also delivered
// directly to the compositor. A second state query ensures that draining did
// not race with a key press. A nil map means the device became non-idle and
// must remain an ungrabbed candidate.
func (m *Manager) acquireIdleDevice(epfd int, device *physicalDevice) (map[uint16]bool, error) {
	pressed, err := currentKeys(device.fd)
	if err != nil {
		return nil, fmt.Errorf("EVIOCGKEY before EVIOCGRAB: %w", err)
	}
	if len(pressed) != 0 {
		return nil, nil
	}
	if err := discardQueuedEvents(device.fd); err != nil {
		return nil, fmt.Errorf("discard pre-grab events: %w", err)
	}
	pressed, err = currentKeys(device.fd)
	if err != nil {
		return nil, fmt.Errorf("EVIOCGKEY after draining: %w", err)
	}
	if len(pressed) != 0 {
		return nil, nil
	}
	if err := grab(device.fd, true); err != nil {
		return nil, fmt.Errorf("EVIOCGRAB: %w", err)
	}
	if err := addEpollFD(epfd, device.fd); err != nil {
		_ = grab(device.fd, false)
		return nil, fmt.Errorf("epoll add keyboard: %w", err)
	}
	return pressed, nil
}

func (m *Manager) readReady(ctx context.Context, device *physicalDevice, buffer []byte, output chan<- input.Message) (bool, error) {
	for {
		count, err := syscall.Read(device.fd, buffer)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				return false, nil
			}
			if err == syscall.ENODEV || err == syscall.EIO {
				return true, nil
			}
			return false, err
		}
		if count == 0 {
			return true, nil
		}
		events, err := decodeEvents(buffer[:count])
		if err != nil {
			return false, err
		}
		for _, event := range events {
			if device.dropping {
				if event.Type == input.EVSyn && event.Code == input.SynReport {
					pressed, stateErr := currentKeys(device.fd)
					if stateErr != nil {
						return false, fmt.Errorf("EVIOCGKEY after SYN_DROPPED: %w", stateErr)
					}
					if err := sendMessage(ctx, output, input.Message{
						Resync: &input.Resync{Device: device.path, Pressed: pressed},
					}); err != nil {
						return false, err
					}
					device.dropping = false
				}
				continue
			}
			if event.Type == input.EVSyn && event.Code == input.SynDropped {
				device.frame = device.frame[:0]
				device.dropping = true
				continue
			}
			device.frame = append(device.frame, event)
			if event.Type == input.EVSyn && event.Code == input.SynReport {
				frame := input.Frame{Device: device.path, Events: append([]input.Event(nil), device.frame...)}
				if err := sendMessage(ctx, output, input.Message{Frame: &frame}); err != nil {
					return false, err
				}
				device.frame = device.frame[:0]
			}
		}
	}
}

func sendMessage(ctx context.Context, output chan<- input.Message, message input.Message) error {
	select {
	case output <- message:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) syncDeviceLEDs(device *physicalDevice) error {
	wrote := false
	for code := uint16(0); code <= input.LEDScrollLock; code++ {
		if !device.leds[code] || !m.locks.Known(code) {
			continue
		}
		value := int32(0)
		if m.locks.LED(code) {
			value = 1
		}
		if err := writeEventFD(device.fd, input.Event{Type: input.EVLed, Code: code, Value: value}); err != nil {
			return err
		}
		wrote = true
	}
	if wrote {
		return writeEventFD(device.fd, input.Event{Type: input.EVSyn, Code: input.SynReport})
	}
	return nil
}

func (m *Manager) closeDevice(epfd int, device *physicalDevice) {
	_ = syscall.EpollCtl(epfd, syscall.EPOLL_CTL_DEL, device.fd, nil)
	_ = grab(device.fd, false)
	_ = syscall.Close(device.fd)
}

func (m *Manager) closeCandidate(device *physicalDevice) {
	// EVIOCGRAB(0) is defensive: most candidates have never been grabbed, but
	// it also makes every failed acquisition path safe after future changes.
	_ = grab(device.fd, false)
	_ = syscall.Close(device.fd)
}
