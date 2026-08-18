//go:build linux

package linuxinput

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
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

// begin records an attempt and reports whether the retry limit permits it.
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
	virtual *VirtualKeyboard
	logger  *log.Logger
	ready   chan struct{}
	locks   LockState
}

type managerState struct {
	manager *Manager
	ctx     context.Context
	output  chan<- input.Message

	epfd      int
	monitorFD int
	wakeRead  int
	wakeWrite int
	stopWake  func() bool
	wakeDone  <-chan struct{}

	byPath         map[string]*physicalDevice
	byFD           map[int]*physicalDevice
	candidates     map[string]*physicalDevice
	retries        map[string]acquisitionRetry
	latestSequence map[string]uint64

	events             []syscall.EpollEvent
	readBuffer         []byte
	udevBuffer         []byte
	nextCandidateCheck time.Time
}

func NewManager(virtual *VirtualKeyboard, logger *log.Logger) *Manager {
	return &Manager{virtual: virtual, logger: logger, ready: make(chan struct{})}
}

// Ready is closed after the udev monitor is active and initial enumeration has
// finished. A keyboard with held keys may still be waiting for an idle handoff.
func (m *Manager) Ready() <-chan struct{} { return m.ready }

// Run owns every physical descriptor, the udev monitor, and the epoll instance,
// and reads the caller-owned virtual descriptor. A single epoll loop preserves
// ordering without one goroutine per keyboard.
func (m *Manager) Run(ctx context.Context, output chan<- input.Message) {
	defer close(output)
	state, err := newManagerState(m, ctx, output)
	if err != nil {
		_ = sendMessage(ctx, output, input.Message{Err: err})
		return
	}
	defer state.close()

	if err := state.enumerate(); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			_ = sendMessage(ctx, output, input.Message{Err: err})
		}
		return
	}
	close(m.ready)

	if err := state.loop(); err != nil &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		_ = sendMessage(ctx, output, input.Message{Err: err})
	}
}

func newManagerState(m *Manager, ctx context.Context, output chan<- input.Message) (*managerState, error) {
	if m.virtual == nil || m.virtual.fd < 0 {
		return nil, errors.New("virtual keyboard is unavailable")
	}
	epfd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("epoll_create1: %w", err)
	}

	monitorFD, err := openUdevMonitor()
	if err != nil {
		_ = syscall.Close(epfd)
		return nil, err
	}
	if err := addEpollFD(epfd, monitorFD); err != nil {
		_ = syscall.Close(monitorFD)
		_ = syscall.Close(epfd)
		return nil, fmt.Errorf("epoll add udev monitor: %w", err)
	}

	state := &managerState{
		manager:        m,
		ctx:            ctx,
		output:         output,
		epfd:           epfd,
		monitorFD:      monitorFD,
		wakeRead:       -1,
		wakeWrite:      -1,
		byPath:         make(map[string]*physicalDevice),
		byFD:           make(map[int]*physicalDevice),
		candidates:     make(map[string]*physicalDevice),
		retries:        make(map[string]acquisitionRetry),
		latestSequence: make(map[string]uint64),
		events:         make([]syscall.EpollEvent, 32),
		readBuffer:     make([]byte, kernelEventSize*eventBufferSize),
		udevBuffer:     make([]byte, udevBufferSize),
	}
	if err := state.initWake(); err != nil {
		state.close()
		return nil, err
	}
	if err := addEpollFD(epfd, m.virtual.fd); err != nil {
		state.close()
		return nil, fmt.Errorf("epoll add virtual keyboard: %w", err)
	}
	return state, nil
}

// initWake registers a cancellation pipe and arranges for context cancellation
// to wake the manager's otherwise indefinite epoll wait.
func (s *managerState) initWake() error {
	var pipe [2]int
	if err := syscall.Pipe2(pipe[:], syscall.O_NONBLOCK|syscall.O_CLOEXEC); err != nil {
		return fmt.Errorf("create manager wake pipe: %w", err)
	}
	if err := addEpollFD(s.epfd, pipe[0]); err != nil {
		_ = syscall.Close(pipe[1])
		_ = syscall.Close(pipe[0])
		return fmt.Errorf("epoll add manager wake pipe: %w", err)
	}
	s.wakeRead = pipe[0]
	s.wakeWrite = pipe[1]
	done := make(chan struct{})
	s.wakeDone = done
	s.stopWake = context.AfterFunc(s.ctx, func() {
		defer close(done)
		for {
			_, err := syscall.Write(pipe[1], []byte{1})
			if err == syscall.EINTR {
				continue
			}
			return
		}
	})
	return nil
}

// close releases every keyboard, the udev monitor, and the epoll instance owned by this run.
func (s *managerState) close() {
	if s.stopWake != nil {
		stopWake := s.stopWake
		s.stopWake = nil
		if !stopWake() && s.wakeDone != nil {
			<-s.wakeDone
		}
	}
	for _, device := range s.byPath {
		s.removeActive(device)
	}
	for _, device := range s.candidates {
		s.removeCandidate(device)
	}
	if s.wakeWrite >= 0 {
		_ = syscall.Close(s.wakeWrite)
		s.wakeWrite = -1
	}
	if s.wakeRead >= 0 {
		_ = syscall.Close(s.wakeRead)
		s.wakeRead = -1
	}
	if s.monitorFD >= 0 {
		_ = syscall.Close(s.monitorFD)
		s.monitorFD = -1
	}
	if s.epfd >= 0 {
		_ = syscall.Close(s.epfd)
		s.epfd = -1
	}
}

// removeActive unregisters, ungrabs, closes, and forgets an active keyboard.
func (s *managerState) removeActive(device *physicalDevice) {
	s.manager.closeDevice(s.epfd, device)
	delete(s.byPath, device.path)
	delete(s.byFD, device.fd)
}

// removeCandidate closes and forgets an ungrabbed keyboard candidate.
func (s *managerState) removeCandidate(device *physicalDevice) {
	s.manager.closeCandidate(device)
	delete(s.candidates, device.path)
}

// syncLEDs writes the current lock state to every active keyboard, logging failures.
func (s *managerState) syncLEDs() {
	for _, device := range s.byPath {
		if err := s.manager.syncDeviceLEDs(device); err != nil {
			s.manager.logger.Printf("synchronize LEDs on %s: %v", device.path, err)
		}
	}
}

// scheduleRetry arranges another acquisition attempt unless the generation exhausted its limit.
func (s *managerState) scheduleRetry(path string, generation deviceGeneration) {
	state := s.retries[path]
	if state.generation != generation {
		state = acquisitionRetry{generation: generation}
	}
	if state.attempts >= maxReacquisitionTries {
		state.scheduled = false
		s.retries[path] = state
		s.manager.logger.Printf("giving up reacquiring %s after %d attempts", path, state.attempts)
		return
	}
	state.deadline = time.Now().Add(acquisitionRetryDelay)
	state.scheduled = true
	s.retries[path] = state
}

// beginRetry records an attempt and reports whether its generation may still be retried.
func (s *managerState) beginRetry(path string, generation deviceGeneration) bool {
	state, tracked := s.retries[path]
	if !tracked {
		return true
	}
	if state.generation != generation {
		state = acquisitionRetry{generation: generation}
	}
	if !state.begin() {
		return false
	}
	s.retries[path] = state
	return true
}

// activate grabs an idle candidate and announces its initial state.
// A nil error may also mean the candidate was no longer idle and remains pending.
func (s *managerState) activate(device *physicalDevice) error {
	pressed, err := s.manager.acquireIdleDevice(s.epfd, device)
	if err != nil {
		return err
	}
	if pressed == nil {
		return nil
	}
	delete(s.candidates, device.path)
	delete(s.retries, device.path)
	s.byPath[device.path] = device
	s.byFD[device.fd] = device
	if err := s.manager.syncDeviceLEDs(device); err != nil {
		s.manager.logger.Printf("initialize LEDs on %s: %v", device.path, err)
	}
	s.manager.logger.Printf("grabbed idle keyboard %s", device.path)
	return sendMessage(s.ctx, s.output, input.Message{
		Resync: &input.Resync{Device: device.path, Pressed: pressed},
	})
}

// acceptCandidate tracks a keyboard and activates it immediately when idle.
// Query and acquisition failures are handled locally by scheduling a retry.
func (s *managerState) acceptCandidate(device *physicalDevice) error {
	path := device.path
	generation := device.generation
	s.candidates[path] = device
	pressed, err := currentKeys(device.fd)
	if err != nil {
		s.removeCandidate(device)
		s.manager.logger.Printf("query initial state for %s; closed for retry: %v", path, err)
		s.scheduleRetry(path, generation)
		return nil
	}
	if len(pressed) != 0 {
		s.manager.logger.Printf("keyboard %s has held keys; waiting for idle state before EVIOCGRAB", path)
		return nil
	}
	if err := s.activate(device); err != nil {
		s.removeCandidate(device)
		s.manager.logger.Printf("acquire %s; closed for retry: %v", path, err)
		s.scheduleRetry(path, generation)
	}
	return nil
}

// consider opens and classifies one device generation for candidacy.
// Expected open and probe failures are handled locally; an error means processing must stop.
func (s *managerState) consider(path string, generation deviceGeneration) error {
	if _, exists := s.byPath[path]; exists {
		return nil
	}
	if _, exists := s.candidates[path]; exists {
		return nil
	}
	device, err := s.manager.openCandidate(path, generation)
	if err != nil {
		if errors.Is(err, errEventNodeChanged) {
			delete(s.retries, path)
			s.manager.logger.Printf("ignore stale generation for %s: %v", path, err)
			return nil
		}
		if !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM) {
			s.manager.logger.Printf("defer %s: %v", path, err)
		}
		s.scheduleRetry(path, generation)
		return nil
	}
	if device == nil {
		delete(s.retries, path)
		return nil
	}
	return s.acceptCandidate(device)
}

// handleAdd reconciles an add event with tracked generations and considers the new device.
// It returns an error only when processing the event must stop.
func (s *managerState) handleAdd(event deviceEvent) error {
	generation := generationFromEvent(event)
	if state, exists := s.retries[event.path]; exists && state.generation.devPath == generation.devPath && !state.generation.observedAdd {
		state.generation = adoptObservedAdd(state.generation, generation)
		generation = state.generation
		s.retries[event.path] = state
	}
	if device := s.byPath[event.path]; device != nil && device.generation.devPath == generation.devPath && !device.generation.observedAdd {
		device.generation = adoptObservedAdd(device.generation, generation)
		return nil
	}
	if device := s.candidates[event.path]; device != nil && device.generation.devPath == generation.devPath && !device.generation.observedAdd {
		device.generation = adoptObservedAdd(device.generation, generation)
		if state, exists := s.retries[event.path]; exists {
			state.generation = device.generation
			s.retries[event.path] = state
		}
		return nil
	}
	if device := s.byPath[event.path]; device != nil {
		s.removeActive(device)
		if err := sendMessage(s.ctx, s.output, input.Message{Removed: event.path}); err != nil {
			return err
		}
	}
	if device := s.candidates[event.path]; device != nil {
		s.removeCandidate(device)
	}
	if _, exists := s.byPath[event.path]; exists {
		return nil
	}
	if _, exists := s.candidates[event.path]; exists {
		return nil
	}
	if !s.beginRetry(event.path, generation) {
		return nil
	}
	return s.consider(event.path, generation)
}

// handleRemove discards matching retry, candidate, and active state for a remove event.
// It returns an error if announcing an active keyboard's removal fails.
func (s *managerState) handleRemove(event deviceEvent) error {
	if state, exists := s.retries[event.path]; exists && generationMatchesRemoval(state.generation, event) {
		delete(s.retries, event.path)
	}
	if device := s.candidates[event.path]; device != nil && generationMatchesRemoval(device.generation, event) {
		s.removeCandidate(device)
	}
	device := s.byPath[event.path]
	if device == nil || !generationMatchesRemoval(device.generation, event) {
		return nil
	}
	s.removeActive(device)
	return sendMessage(s.ctx, s.output, input.Message{Removed: event.path})
}

// enumerate discovers the event nodes present at startup and queues or accepts keyboards.
// It returns an error when initial ordering cannot be established or processing must stop.
func (s *managerState) enumerate() error {
	// The monitor is subscribed before enumeration, so additions racing with
	// the glob remain queued on the netlink descriptor.
	paths, _ := filepath.Glob("/dev/input/event*")
	for _, path := range paths {
		device, generation, err := s.manager.openEnumeratedCandidate(path)
		if err != nil {
			if errors.Is(err, errKernelUeventSequence) {
				return fmt.Errorf("establish uevent ordering for initial device %s: %w", path, err)
			}
			if errors.Is(err, errEventNodeChanged) {
				s.manager.logger.Printf("skip changed initial device %s; queued udev events will identify its replacement", path)
				continue
			}
			s.manager.logger.Printf("defer initial device %s: %v", path, err)
			if generation.devPath != "" {
				s.scheduleRetry(path, generation)
			}
			continue
		}
		if device == nil {
			continue
		}
		if err := s.acceptCandidate(device); err != nil {
			return err
		}
	}
	s.nextCandidateCheck = time.Now()
	return nil
}

// loop runs device maintenance and dispatches epoll events until cancellation or failure.
// It returns nil or the context error on cancellation, and other fatal failures unchanged.
func (s *managerState) loop() error {
	for {
		select {
		case <-s.ctx.Done():
			return nil
		default:
		}
		now := time.Now()
		if err := s.checkCandidates(now); err != nil {
			return err
		}
		if err := s.processRetries(now); err != nil {
			return err
		}

		count, err := syscall.EpollWait(s.epfd, s.events, s.epollTimeout(time.Now()))
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return fmt.Errorf("epoll_wait: %w", err)
		}
		for _, event := range s.events[:count] {
			if err := s.handleReady(event); err != nil {
				return err
			}
		}
	}
}

// checkCandidates activates candidates that have become idle and reschedules failed acquisitions.
// It returns an error only when candidate processing must stop.
func (s *managerState) checkCandidates(now time.Time) error {
	if now.Before(s.nextCandidateCheck) {
		return nil
	}
	for _, device := range s.candidates {
		pressed, err := currentKeys(device.fd)
		if err != nil {
			s.removeCandidate(device)
			if !errors.Is(err, syscall.ENODEV) && !errors.Is(err, syscall.ENOENT) {
				s.manager.logger.Printf("query pending state for %s; closed for retry: %v", device.path, err)
				s.scheduleRetry(device.path, device.generation)
			}
			continue
		}
		if len(pressed) != 0 {
			continue
		}
		if err := s.activate(device); err != nil {
			s.removeCandidate(device)
			s.manager.logger.Printf("acquire %s; closed for retry: %v", device.path, err)
			s.scheduleRetry(device.path, device.generation)
		}
	}
	s.nextCandidateCheck = now.Add(candidateCheckInterval)
	return nil
}

// processRetries runs due acquisition retries and returns a fatal processing error.
func (s *managerState) processRetries(now time.Time) error {
	for path, state := range s.retries {
		if !state.scheduled {
			continue
		}
		if now.Before(state.deadline) {
			continue
		}
		if !s.beginRetry(path, state.generation) {
			continue
		}
		if err := s.consider(path, state.generation); err != nil {
			return err
		}
	}
	return nil
}

// epollTimeout returns milliseconds until the earliest pending maintenance deadline.
// It returns -1 when the manager can wait indefinitely and 0 when work is due.
func (s *managerState) epollTimeout(now time.Time) int {
	var deadline time.Time
	if len(s.candidates) != 0 {
		deadline = s.nextCandidateCheck
	}
	for _, state := range s.retries {
		if !state.scheduled || (!deadline.IsZero() && !state.deadline.Before(deadline)) {
			continue
		}
		deadline = state.deadline
	}
	if deadline.IsZero() {
		return -1
	}
	delay := deadline.Sub(now)
	if delay <= 0 {
		return 0
	}
	return int((delay + time.Millisecond - 1) / time.Millisecond)
}

// handleReady dispatches one epoll event to the udev or keyboard handler.
// It returns an error when the run must stop.
func (s *managerState) handleReady(ready syscall.EpollEvent) error {
	fd := int(ready.Fd)
	if fd == s.monitorFD {
		return s.handleUdevReady(ready)
	}
	if fd == s.wakeRead {
		return s.handleWakeReady()
	}
	if fd == s.manager.virtual.fd {
		return s.handleFeedbackReady(ready)
	}
	return s.handleDeviceReady(ready)
}

// handleWakeReady terminates the loop when the cancellation pipe is readable.
func (s *managerState) handleWakeReady() error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return errors.New("manager wake pipe became readable unexpectedly")
}

// handleFeedbackReady drains compositor output from the virtual keyboard and
// synchronizes changed lock state to active physical keyboards.
func (s *managerState) handleFeedbackReady(ready syscall.EpollEvent) error {
	if ready.Events&(syscall.EPOLLERR|syscall.EPOLLHUP) != 0 {
		return errors.New("virtual keyboard feedback closed")
	}
	var values [3]bool
	var seen [3]bool
	for {
		count, err := syscall.Read(s.manager.virtual.fd, s.readBuffer)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				break
			}
			return fmt.Errorf("read virtual keyboard feedback: %w", err)
		}
		if count == 0 {
			return errors.New("virtual keyboard feedback closed")
		}
		events, err := decodeEvents(s.readBuffer[:count])
		if err != nil {
			return fmt.Errorf("decode virtual keyboard feedback: %w", err)
		}
		for _, event := range events {
			if event.Type != input.EVLed || event.Code > input.LEDScrollLock {
				continue
			}
			values[event.Code] = event.Value != 0
			seen[event.Code] = true
		}
	}
	changed := false
	for code, value := range values {
		if seen[code] {
			changed = s.manager.locks.SetLED(uint16(code), value) || changed
		}
	}
	if !changed {
		return nil
	}
	caps, num, scroll := s.manager.locks.Snapshot()
	s.manager.logger.Printf("compositor lock state caps=%t num=%t scroll=%t", caps, num, scroll)
	s.syncLEDs()
	return nil
}

// handleUdevReady drains and applies sequenced device-management events.
// It returns an error if the monitor fails or an event cannot be processed.
func (s *managerState) handleUdevReady(ready syscall.EpollEvent) error {
	if ready.Events&(syscall.EPOLLERR|syscall.EPOLLHUP) != 0 {
		return errors.New("udev monitor closed")
	}
	events, err := readDeviceEvents(s.monitorFD, s.udevBuffer)
	if err != nil {
		return fmt.Errorf("read udev monitor: %w", err)
	}
	for _, event := range events {
		if !acceptEventSequence(s.latestSequence, event) {
			continue
		}
		if event.action == "remove" {
			if err := s.handleRemove(event); err != nil {
				return err
			}
		} else if err := s.handleAdd(event); err != nil {
			return err
		}
	}
	return nil
}

// handleDeviceReady drains one active keyboard and removes it when disconnected or faulty.
// It returns an error only when cancellation or output delivery must stop the run.
func (s *managerState) handleDeviceReady(ready syscall.EpollEvent) error {
	device := s.byFD[int(ready.Fd)]
	if device == nil {
		return nil
	}
	removed, err := s.manager.readReady(s.ctx, device, s.readBuffer, s.output)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err != nil {
		s.manager.logger.Printf("read %s: %v", device.path, err)
	}
	if !removed && err == nil && ready.Events&(syscall.EPOLLHUP|syscall.EPOLLERR) == 0 {
		return nil
	}
	s.removeActive(device)
	if sendErr := sendMessage(s.ctx, s.output, input.Message{Removed: device.path}); sendErr != nil {
		return sendErr
	}
	if !removed {
		s.scheduleRetry(device.path, device.generation)
	}
	return nil
}

// Opens and probes a known device generation. A nil device with no error means
// the node should be ignored; returned devices own their fd.
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

// Establishes which device an event-node descriptor refers to before probing it.
// The sequence number is sampled only after the descriptor and its sysfs DEVPATH
// have been obtained. Comparing fstat(2) on the descriptor with stat(2) on the
// pathname proves that the pathname still names that descriptor's device at the end
// of the observation window.
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

// Identifies whether an open descriptor is a usable physical keyboard.
// A nil device with no error means it should be ignored; errors close the supplied fd.
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
	if name == m.virtual.name {
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

// Performs the idle check before EVIOCGRAB. The candidate's pre-grab event
// queue is discarded because those events were also delivered directly to the
// userspace input stack. A second state query ensures that draining did
// not race with a key press. A nil map means the device became non-idle and
// must remain an ungrabbed candidate; an error means acquisition failed.
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
	// post-grab state check to detect a keypress that raced with EVIOCGRAB
	pressed, err = currentKeys(device.fd)
	if err != nil {
		_ = grab(device.fd, false)
		return nil, fmt.Errorf("EVIOCGKEY after EVIOCGRAB: %w", err)
	}
	// if EVIOCGKEY reports pressed keys after EVIOCGRAB, keep in the candidate pool;
	// a pre-grab keypress may have already reached the userspace input stack,
	// ungrabbing ensures that the corresponding release arrives through
	// the same device
	if len(pressed) != 0 {
		if err := grab(device.fd, false); err != nil {
			return nil, fmt.Errorf("release non-idle device: %w", err)
		}
		return nil, nil
	}
	if err := addEpollFD(epfd, device.fd); err != nil {
		_ = grab(device.fd, false)
		return nil, fmt.Errorf("epoll add keyboard: %w", err)
	}
	return pressed, nil
}

// Drains available events from one keyboard and emits complete frames or a resync.
// The boolean reports device disappearance or EOF; the error reports read, decode,
// or send failure.
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

// Writes known lock states for the LEDs supported by one keyboard. It returns the first
// write failure, or nil after a successful or unnecessary sync.
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

// Removes an active keyboard from epoll, releases its grab, and closes it.
func (m *Manager) closeDevice(epfd int, device *physicalDevice) {
	_ = syscall.EpollCtl(epfd, syscall.EPOLL_CTL_DEL, device.fd, nil)
	_ = grab(device.fd, false)
	_ = syscall.Close(device.fd)
}

// Defensively releases and closes a candidate keyboard.
func (m *Manager) closeCandidate(device *physicalDevice) {
	// EVIOCGRAB(0) is defensive: most candidates have never been grabbed, but
	// it also makes every failed acquisition path safe after future changes.
	_ = grab(device.fd, false)
	_ = syscall.Close(device.fd)
}
