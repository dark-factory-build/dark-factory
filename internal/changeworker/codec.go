package changeworker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/provider"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const (
	ConfigName           = "change-worker.config"
	AttemptTokenName     = "attempt.token"
	contractVersion      = 2
	HomeName             = "home"
	TempName             = "tmp"
	CheckpointLimit      = 32 << 10
	ConfigLimit          = 256 << 10
	maximumLocatorBytes  = 4096
	maximumRevisionBytes = 4096
)

var ErrInvalidContract = errors.New("Change worker: invalid private contract")

var contractMagic = [4]byte{'D', 'F', 'D', 'C'}

type contractKind byte

const (
	kindWorkerConfig contractKind = iota + 1
	kindSelection
	kindPreparation
	kindPopulation
)

type Config struct {
	Provider             kernel.Provider
	Model                string
	ReasoningEffort      string
	RuntimePath          string
	RuntimeIdentity      runner.FileIdentity
	GitExecutable        string
	FactoryctlExecutable string
	ToolPath             string
	RepositoryRoot       string
	RepositoryIdentity   change.RepositoryIdentity
	Revision             string
	ChangeParent         string
	FinalName            string
	StagingName          string
	AttemptSocket        string
	Retained             *RetainedChange
	// ProviderTask is sealed in one unlinked descriptor before provider exec.
	// It never shares the interactive PTY input stream.
	ProviderTask []byte
}

func (Config) String() string   { return "Change worker config (private)" }
func (Config) GoString() string { return "changeworker.Config{private}" }

// RetainedChange is the exact durable publication authority supplied for a
// retained-at-A retry. It contains no source or staging locator and selects no
// new Git content.
type RetainedChange struct {
	Format     change.ObjectFormat
	Base       change.ObjectID
	Commitment change.Commitment
	EntryCount uint64
	BlobBytes  uint64
	Tree       change.StageIdentity
}

func (RetainedChange) String() string   { return "Retained Change authority (private)" }
func (RetainedChange) GoString() string { return "changeworker.RetainedChange{private}" }

type SelectionReport struct {
	Format     change.ObjectFormat
	Base       change.ObjectID
	Commitment change.Commitment
	EntryCount uint64
	BlobBytes  uint64
	Repository change.RepositoryIdentity
}

func (SelectionReport) String() string   { return "Change selection checkpoint (private)" }
func (SelectionReport) GoString() string { return "changeworker.SelectionReport{private}" }

type PreparationReport struct{ Stage change.StageIdentity }

func (PreparationReport) String() string   { return "Change preparation checkpoint (private)" }
func (PreparationReport) GoString() string { return "changeworker.PreparationReport{private}" }

type PopulationReport struct {
	Identity   change.StageIdentity
	Commitment change.Commitment
	EntryCount uint64
	BlobBytes  uint64
}

func (PopulationReport) String() string   { return "Change population checkpoint (private)" }
func (PopulationReport) GoString() string { return "changeworker.PopulationReport{private}" }

func EncodeConfig(config Config) ([]byte, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	encoded := appendHeader(nil, kindWorkerConfig)
	encoded = binary.BigEndian.AppendUint64(encoded, config.RuntimeIdentity.Device)
	encoded = binary.BigEndian.AppendUint64(encoded, config.RuntimeIdentity.Inode)
	encoded = binary.BigEndian.AppendUint64(encoded, config.RepositoryIdentity.Device())
	encoded = binary.BigEndian.AppendUint64(encoded, config.RepositoryIdentity.Inode())
	encoded = append(encoded, encodeProvider(config.Provider))
	if config.Retained == nil {
		encoded = append(encoded, 0)
	} else {
		encoded = append(encoded, 1, formatByte(config.Retained.Format))
		encoded = append(encoded, config.Retained.Base.Bytes()...)
		encoded = append(encoded, config.Retained.Commitment.Bytes()...)
		encoded = binary.BigEndian.AppendUint64(encoded, config.Retained.EntryCount)
		encoded = binary.BigEndian.AppendUint64(encoded, config.Retained.BlobBytes)
		encoded = binary.BigEndian.AppendUint64(encoded, config.Retained.Tree.Device())
		encoded = binary.BigEndian.AppendUint64(encoded, config.Retained.Tree.Inode())
	}
	for _, value := range []string{
		config.Model, config.ReasoningEffort, config.RuntimePath, config.GitExecutable, config.FactoryctlExecutable, config.ToolPath, config.RepositoryRoot, config.Revision,
		config.ChangeParent, config.FinalName, config.StagingName, config.AttemptSocket,
	} {
		encoded = appendString(encoded, value)
	}
	encoded = binary.BigEndian.AppendUint32(encoded, uint32(len(config.ProviderTask)))
	encoded = append(encoded, config.ProviderTask...)
	if len(encoded) > ConfigLimit {
		return nil, invalidContract(nil)
	}
	return encoded, nil
}

func DecodeConfig(encoded []byte) (Config, error) {
	reader, err := newContractReader(encoded, kindWorkerConfig, ConfigLimit)
	if err != nil {
		return Config{}, err
	}
	device, err := reader.uint64()
	if err != nil {
		return Config{}, err
	}
	inode, err := reader.uint64()
	if err != nil {
		return Config{}, err
	}
	repositoryDevice, err := reader.uint64()
	if err != nil {
		return Config{}, err
	}
	repositoryInode, err := reader.uint64()
	if err != nil {
		return Config{}, err
	}
	repositoryIdentity, err := change.NewRepositoryIdentity(repositoryDevice, repositoryInode)
	if err != nil {
		return Config{}, invalidContract(err)
	}
	providerByte, err := reader.byte()
	if err != nil {
		return Config{}, err
	}
	providerKind, err := decodeProvider(providerByte)
	if err != nil {
		return Config{}, err
	}
	retainedMode, err := reader.byte()
	if err != nil || retainedMode > 1 {
		return Config{}, invalidContract(err)
	}
	var retained *RetainedChange
	if retainedMode == 1 {
		formatCode, readErr := reader.byte()
		if readErr != nil {
			return Config{}, readErr
		}
		format, readErr := decodeFormat(formatCode)
		if readErr != nil {
			return Config{}, readErr
		}
		baseBytes, readErr := reader.fixed(format.OIDLength())
		if readErr != nil {
			return Config{}, readErr
		}
		base, readErr := change.NewObjectID(format, baseBytes)
		if readErr != nil {
			return Config{}, invalidContract(readErr)
		}
		commitmentBytes, readErr := reader.fixed(32)
		if readErr != nil {
			return Config{}, readErr
		}
		commitment, readErr := change.ParseCommitment(commitmentBytes)
		if readErr != nil {
			return Config{}, invalidContract(readErr)
		}
		entries, readErr := reader.uint64()
		if readErr != nil {
			return Config{}, readErr
		}
		blobs, readErr := reader.uint64()
		if readErr != nil {
			return Config{}, readErr
		}
		treeDevice, readErr := reader.uint64()
		if readErr != nil {
			return Config{}, readErr
		}
		treeInode, readErr := reader.uint64()
		if readErr != nil {
			return Config{}, readErr
		}
		tree, readErr := change.NewStageIdentity(treeDevice, treeInode)
		if readErr != nil {
			return Config{}, invalidContract(readErr)
		}
		retained = &RetainedChange{Format: format, Base: base, Commitment: commitment, EntryCount: entries, BlobBytes: blobs, Tree: tree}
	}
	values := make([]string, 12)
	for index := range values {
		values[index], err = reader.string()
		if err != nil {
			return Config{}, err
		}
	}
	task, err := reader.bytes(runner.MaxProviderTaskBytes)
	if err != nil || !reader.done() {
		return Config{}, invalidContract(err)
	}
	config := Config{
		Provider: providerKind, Model: values[0], ReasoningEffort: values[1],
		RuntimePath: values[2], RuntimeIdentity: runner.FileIdentity{Device: device, Inode: inode},
		GitExecutable: values[3], FactoryctlExecutable: values[4], ToolPath: values[5], RepositoryRoot: values[6], RepositoryIdentity: repositoryIdentity, Revision: values[7],
		ChangeParent: values[8], FinalName: values[9], StagingName: values[10],
		AttemptSocket: values[11], Retained: retained, ProviderTask: task,
	}
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func validateConfig(config Config) error {
	paths := []string{config.RuntimePath, config.GitExecutable, config.FactoryctlExecutable, config.RepositoryRoot, config.ChangeParent, config.AttemptSocket}
	for _, path := range paths {
		if !validAbsolute(path, maximumLocatorBytes) {
			return invalidContract(nil)
		}
	}
	if len(config.AttemptSocket) > install.MaxSocketPathBytes || config.RuntimeIdentity.Device == 0 || config.RuntimeIdentity.Inode == 0 ||
		encodeProvider(config.Provider) == 0 ||
		kernel.ValidateProviderLaunchControls(config.Provider, config.Model, config.ReasoningEffort) != nil || provider.ValidateToolPath(config.ToolPath) != nil ||
		!validText(config.Revision, maximumRevisionBytes) || !validChangeName(config.FinalName) || !validChangeName(config.StagingName) ||
		config.FinalName == config.StagingName || provider.ValidateTask(config.Provider, config.ProviderTask) != nil {
		return invalidContract(nil)
	}
	if _, err := change.NewRepositoryIdentity(config.RepositoryIdentity.Device(), config.RepositoryIdentity.Inode()); err != nil {
		return invalidContract(err)
	}
	if config.Retained != nil {
		selection := SelectionReport{
			Format: config.Retained.Format, Base: config.Retained.Base, Commitment: config.Retained.Commitment,
			EntryCount: config.Retained.EntryCount, BlobBytes: config.Retained.BlobBytes, Repository: config.RepositoryIdentity,
		}
		population := PopulationReport{
			Identity: config.Retained.Tree, Commitment: config.Retained.Commitment,
			EntryCount: config.Retained.EntryCount, BlobBytes: config.Retained.BlobBytes,
		}
		if ValidateSelectionReport(selection) != nil || ValidatePopulationReport(population) != nil {
			return invalidContract(nil)
		}
	}
	return nil
}

func encodeProvider(value kernel.Provider) byte {
	switch value {
	case kernel.ProviderShell:
		return 1
	case kernel.ProviderClaudeCode:
		return 2
	case kernel.ProviderCodex:
		return 3
	default:
		return 0
	}
}

func decodeProvider(value byte) (kernel.Provider, error) {
	switch value {
	case 1:
		return kernel.ProviderShell, nil
	case 2:
		return kernel.ProviderClaudeCode, nil
	case 3:
		return kernel.ProviderCodex, nil
	default:
		return 0, invalidContract(nil)
	}
}

func EncodeSelectionReport(checkpoint SelectionReport) ([]byte, error) {
	if err := ValidateSelectionReport(checkpoint); err != nil {
		return nil, err
	}
	encoded := appendHeader(nil, kindSelection)
	encoded = append(encoded, formatByte(checkpoint.Format))
	encoded = append(encoded, checkpoint.Base.Bytes()...)
	encoded = append(encoded, checkpoint.Commitment.Bytes()...)
	encoded = binary.BigEndian.AppendUint64(encoded, checkpoint.EntryCount)
	encoded = binary.BigEndian.AppendUint64(encoded, checkpoint.BlobBytes)
	encoded = binary.BigEndian.AppendUint64(encoded, checkpoint.Repository.Device())
	encoded = binary.BigEndian.AppendUint64(encoded, checkpoint.Repository.Inode())
	return boundedCheckpoint(encoded)
}

func DecodeSelectionReport(encoded []byte) (SelectionReport, error) {
	reader, err := newContractReader(encoded, kindSelection, CheckpointLimit)
	if err != nil {
		return SelectionReport{}, err
	}
	formatCode, err := reader.byte()
	if err != nil {
		return SelectionReport{}, err
	}
	format, err := decodeFormat(formatCode)
	if err != nil {
		return SelectionReport{}, err
	}
	baseBytes, err := reader.fixed(format.OIDLength())
	if err != nil {
		return SelectionReport{}, err
	}
	base, err := change.NewObjectID(format, baseBytes)
	if err != nil {
		return SelectionReport{}, invalidContract(err)
	}
	commitmentBytes, err := reader.fixed(32)
	if err != nil {
		return SelectionReport{}, err
	}
	commitment, err := change.ParseCommitment(commitmentBytes)
	if err != nil {
		return SelectionReport{}, invalidContract(err)
	}
	entries, err := reader.uint64()
	if err != nil {
		return SelectionReport{}, err
	}
	blobs, err := reader.uint64()
	if err != nil {
		return SelectionReport{}, err
	}
	device, err := reader.uint64()
	if err != nil {
		return SelectionReport{}, err
	}
	inode, err := reader.uint64()
	if err != nil || !reader.done() {
		return SelectionReport{}, invalidContract(err)
	}
	repository, err := change.NewRepositoryIdentity(device, inode)
	if err != nil {
		return SelectionReport{}, invalidContract(err)
	}
	checkpoint := SelectionReport{Format: format, Base: base, Commitment: commitment, EntryCount: entries, BlobBytes: blobs, Repository: repository}
	if err := ValidateSelectionReport(checkpoint); err != nil {
		return SelectionReport{}, err
	}
	return checkpoint, nil
}

func ValidateSelectionReport(checkpoint SelectionReport) error {
	if checkpoint.Format.OIDLength() == 0 || checkpoint.Base.Format() != checkpoint.Format || len(checkpoint.Base.Bytes()) != checkpoint.Format.OIDLength() ||
		len(checkpoint.Commitment.Bytes()) != 32 || checkpoint.EntryCount > change.MaxEntryCount || checkpoint.BlobBytes > change.MaxTotalBlobBytes {
		return invalidContract(nil)
	}
	if _, err := change.NewRepositoryIdentity(checkpoint.Repository.Device(), checkpoint.Repository.Inode()); err != nil {
		return invalidContract(err)
	}
	return nil
}

func EncodePreparationReport(checkpoint PreparationReport) ([]byte, error) {
	if _, err := change.NewStageIdentity(checkpoint.Stage.Device(), checkpoint.Stage.Inode()); err != nil {
		return nil, invalidContract(err)
	}
	encoded := appendHeader(nil, kindPreparation)
	encoded = binary.BigEndian.AppendUint64(encoded, checkpoint.Stage.Device())
	encoded = binary.BigEndian.AppendUint64(encoded, checkpoint.Stage.Inode())
	return boundedCheckpoint(encoded)
}

func DecodePreparationReport(encoded []byte) (PreparationReport, error) {
	reader, err := newContractReader(encoded, kindPreparation, CheckpointLimit)
	if err != nil {
		return PreparationReport{}, err
	}
	device, err := reader.uint64()
	if err != nil {
		return PreparationReport{}, err
	}
	inode, err := reader.uint64()
	if err != nil || !reader.done() {
		return PreparationReport{}, invalidContract(err)
	}
	stage, err := change.NewStageIdentity(device, inode)
	if err != nil {
		return PreparationReport{}, invalidContract(err)
	}
	return PreparationReport{Stage: stage}, nil
}

func EncodePopulationReport(checkpoint PopulationReport) ([]byte, error) {
	if err := ValidatePopulationReport(checkpoint); err != nil {
		return nil, err
	}
	encoded := appendHeader(nil, kindPopulation)
	encoded = binary.BigEndian.AppendUint64(encoded, checkpoint.Identity.Device())
	encoded = binary.BigEndian.AppendUint64(encoded, checkpoint.Identity.Inode())
	encoded = append(encoded, checkpoint.Commitment.Bytes()...)
	encoded = binary.BigEndian.AppendUint64(encoded, checkpoint.EntryCount)
	encoded = binary.BigEndian.AppendUint64(encoded, checkpoint.BlobBytes)
	return boundedCheckpoint(encoded)
}

func DecodePopulationReport(encoded []byte) (PopulationReport, error) {
	reader, err := newContractReader(encoded, kindPopulation, CheckpointLimit)
	if err != nil {
		return PopulationReport{}, err
	}
	device, err := reader.uint64()
	if err != nil {
		return PopulationReport{}, err
	}
	inode, err := reader.uint64()
	if err != nil {
		return PopulationReport{}, err
	}
	stage, err := change.NewStageIdentity(device, inode)
	if err != nil {
		return PopulationReport{}, invalidContract(err)
	}
	commitmentBytes, err := reader.fixed(32)
	if err != nil {
		return PopulationReport{}, err
	}
	commitment, err := change.ParseCommitment(commitmentBytes)
	if err != nil {
		return PopulationReport{}, invalidContract(err)
	}
	entries, err := reader.uint64()
	if err != nil {
		return PopulationReport{}, err
	}
	blobs, err := reader.uint64()
	if err != nil || !reader.done() {
		return PopulationReport{}, invalidContract(err)
	}
	checkpoint := PopulationReport{Identity: stage, Commitment: commitment, EntryCount: entries, BlobBytes: blobs}
	if err := ValidatePopulationReport(checkpoint); err != nil {
		return PopulationReport{}, err
	}
	return checkpoint, nil
}

func ValidatePopulationReport(checkpoint PopulationReport) error {
	if _, err := change.NewStageIdentity(checkpoint.Identity.Device(), checkpoint.Identity.Inode()); err != nil {
		return invalidContract(err)
	}
	if len(checkpoint.Commitment.Bytes()) != 32 || checkpoint.EntryCount > change.MaxEntryCount || checkpoint.BlobBytes > change.MaxTotalBlobBytes {
		return invalidContract(nil)
	}
	return nil
}

func boundedCheckpoint(encoded []byte) ([]byte, error) {
	if len(encoded) > CheckpointLimit {
		return nil, invalidContract(nil)
	}
	return encoded, nil
}

func appendHeader(encoded []byte, kind contractKind) []byte {
	encoded = append(encoded, contractMagic[:]...)
	return append(encoded, contractVersion, byte(kind), 0, 0)
}

func appendString(encoded []byte, value string) []byte {
	encoded = binary.BigEndian.AppendUint16(encoded, uint16(len(value)))
	return append(encoded, value...)
}

func formatByte(format change.ObjectFormat) byte {
	switch format.Name() {
	case "sha1":
		return 1
	case "sha256":
		return 2
	default:
		return 0
	}
}

func decodeFormat(value byte) (change.ObjectFormat, error) {
	switch value {
	case 1:
		format, err := change.NewObjectFormat("sha1")
		if err != nil {
			return 0, invalidContract(err)
		}
		return format, nil
	case 2:
		format, err := change.NewObjectFormat("sha256")
		if err != nil {
			return 0, invalidContract(err)
		}
		return format, nil
	default:
		return 0, invalidContract(nil)
	}
}

func validText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validAbsolute(value string, maximum int) bool {
	return validText(value, maximum) && filepath.IsAbs(value) && filepath.Clean(value) == value && value != string(filepath.Separator)
}

func validChangeName(value string) bool {
	return validText(value, 255) && filepath.Base(value) == value && value != "." && value != ".." && !strings.EqualFold(value, ".git")
}

type contractReader struct {
	value  []byte
	offset int
}

func newContractReader(encoded []byte, want contractKind, maximum int) (*contractReader, error) {
	if len(encoded) < 8 || len(encoded) > maximum || !bytes.Equal(encoded[:4], contractMagic[:]) || encoded[4] != contractVersion || contractKind(encoded[5]) != want || encoded[6] != 0 || encoded[7] != 0 {
		return nil, invalidContract(nil)
	}
	return &contractReader{value: bytes.Clone(encoded), offset: 8}, nil
}

func (reader *contractReader) fixed(length int) ([]byte, error) {
	if reader == nil || length < 0 || length > len(reader.value)-reader.offset {
		return nil, invalidContract(io.ErrUnexpectedEOF)
	}
	value := bytes.Clone(reader.value[reader.offset : reader.offset+length])
	reader.offset += length
	return value, nil
}

func (reader *contractReader) byte() (byte, error) {
	value, err := reader.fixed(1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

func (reader *contractReader) uint64() (uint64, error) {
	value, err := reader.fixed(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(value), nil
}

func (reader *contractReader) string() (string, error) {
	lengthBytes, err := reader.fixed(2)
	if err != nil {
		return "", err
	}
	length := int(binary.BigEndian.Uint16(lengthBytes))
	value, err := reader.fixed(length)
	if err != nil || !utf8.Valid(value) || bytes.IndexByte(value, 0) >= 0 {
		return "", invalidContract(err)
	}
	return string(value), nil
}

func (reader *contractReader) bytes(maximum int) ([]byte, error) {
	lengthBytes, err := reader.fixed(4)
	if err != nil {
		return nil, err
	}
	length := uint64(binary.BigEndian.Uint32(lengthBytes))
	if length > uint64(maximum) || length > math.MaxInt {
		return nil, invalidContract(nil)
	}
	return reader.fixed(int(length))
}

func (reader *contractReader) done() bool { return reader != nil && reader.offset == len(reader.value) }

func invalidContract(error) error { return ErrInvalidContract }
