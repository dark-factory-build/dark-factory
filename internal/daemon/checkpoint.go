package daemon

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

const (
	contractVersion      = 1
	checkpointLimit      = 32 << 10
	workerConfigLimit    = 256 << 10
	workerInputLimit     = 128 << 10
	maximumLocatorBytes  = 4096
	maximumRevisionBytes = 4096
	maximumSocketBytes   = 103
)

var contractMagic = [4]byte{'D', 'F', 'D', 'C'}

type contractKind byte

const (
	kindWorkerConfig contractKind = iota + 1
	kindSelection
	kindPreparation
	kindPopulation
)

type workerConfig struct {
	RuntimePath        string
	RuntimeIdentity    runner.FileIdentity
	GitExecutable      string
	RepositoryRoot     string
	RepositoryIdentity change.RepositoryIdentity
	Revision           string
	ChangeParent       string
	FinalName          string
	StagingName        string
	ProviderProgram    string
	ProviderHome       string
	ProviderTemp       string
	AttemptSocket      string
	AttemptTokenPath   string
	StartupInput       []byte
}

func (workerConfig) String() string   { return "Change worker config (private)" }
func (workerConfig) GoString() string { return "daemon.workerConfig{private}" }

type selectionCheckpoint struct {
	Format     change.ObjectFormat
	Base       change.ObjectID
	Commitment change.Commitment
	EntryCount uint64
	BlobBytes  uint64
	Repository change.RepositoryIdentity
}

func (selectionCheckpoint) String() string   { return "Change selection checkpoint (private)" }
func (selectionCheckpoint) GoString() string { return "daemon.selectionCheckpoint{private}" }

type preparationCheckpoint struct{ Stage change.StageIdentity }

func (preparationCheckpoint) String() string   { return "Change preparation checkpoint (private)" }
func (preparationCheckpoint) GoString() string { return "daemon.preparationCheckpoint{private}" }

type populationCheckpoint struct {
	Identity   change.StageIdentity
	Commitment change.Commitment
	EntryCount uint64
	BlobBytes  uint64
}

func (populationCheckpoint) String() string   { return "Change population checkpoint (private)" }
func (populationCheckpoint) GoString() string { return "daemon.populationCheckpoint{private}" }

func encodeWorkerConfig(config workerConfig) ([]byte, error) {
	if err := validateWorkerConfig(config); err != nil {
		return nil, err
	}
	encoded := appendHeader(nil, kindWorkerConfig)
	encoded = binary.BigEndian.AppendUint64(encoded, config.RuntimeIdentity.Device)
	encoded = binary.BigEndian.AppendUint64(encoded, config.RuntimeIdentity.Inode)
	encoded = binary.BigEndian.AppendUint64(encoded, config.RepositoryIdentity.Device())
	encoded = binary.BigEndian.AppendUint64(encoded, config.RepositoryIdentity.Inode())
	for _, value := range []string{
		config.RuntimePath, config.GitExecutable, config.RepositoryRoot, config.Revision,
		config.ChangeParent, config.FinalName, config.StagingName, config.ProviderProgram,
		config.ProviderHome, config.ProviderTemp, config.AttemptSocket, config.AttemptTokenPath,
	} {
		encoded = appendString(encoded, value)
	}
	encoded = binary.BigEndian.AppendUint32(encoded, uint32(len(config.StartupInput)))
	encoded = append(encoded, config.StartupInput...)
	if len(encoded) > workerConfigLimit {
		return nil, invalidContract(nil)
	}
	return encoded, nil
}

func decodeWorkerConfig(encoded []byte) (workerConfig, error) {
	reader, err := newContractReader(encoded, kindWorkerConfig, workerConfigLimit)
	if err != nil {
		return workerConfig{}, err
	}
	device, err := reader.uint64()
	if err != nil {
		return workerConfig{}, err
	}
	inode, err := reader.uint64()
	if err != nil {
		return workerConfig{}, err
	}
	repositoryDevice, err := reader.uint64()
	if err != nil {
		return workerConfig{}, err
	}
	repositoryInode, err := reader.uint64()
	if err != nil {
		return workerConfig{}, err
	}
	repositoryIdentity, err := change.NewRepositoryIdentity(repositoryDevice, repositoryInode)
	if err != nil {
		return workerConfig{}, invalidContract(err)
	}
	values := make([]string, 12)
	for index := range values {
		values[index], err = reader.string()
		if err != nil {
			return workerConfig{}, err
		}
	}
	input, err := reader.bytes(workerInputLimit)
	if err != nil || !reader.done() {
		return workerConfig{}, invalidContract(err)
	}
	config := workerConfig{
		RuntimePath: values[0], RuntimeIdentity: runner.FileIdentity{Device: device, Inode: inode},
		GitExecutable: values[1], RepositoryRoot: values[2], RepositoryIdentity: repositoryIdentity, Revision: values[3],
		ChangeParent: values[4], FinalName: values[5], StagingName: values[6],
		ProviderProgram: values[7], ProviderHome: values[8], ProviderTemp: values[9],
		AttemptSocket: values[10], AttemptTokenPath: values[11], StartupInput: input,
	}
	if err := validateWorkerConfig(config); err != nil {
		return workerConfig{}, err
	}
	return config, nil
}

func validateWorkerConfig(config workerConfig) error {
	paths := []string{config.RuntimePath, config.GitExecutable, config.RepositoryRoot, config.ChangeParent, config.ProviderProgram, config.ProviderHome, config.ProviderTemp, config.AttemptSocket, config.AttemptTokenPath}
	for _, path := range paths {
		if !validAbsolute(path, maximumLocatorBytes) {
			return invalidContract(nil)
		}
	}
	if len(config.AttemptSocket) > maximumSocketBytes || config.RuntimeIdentity.Device == 0 || config.RuntimeIdentity.Inode == 0 ||
		!validText(config.Revision, maximumRevisionBytes) || !validChangeName(config.FinalName) || !validChangeName(config.StagingName) ||
		config.FinalName == config.StagingName || len(config.StartupInput) > workerInputLimit {
		return invalidContract(nil)
	}
	if _, err := change.NewRepositoryIdentity(config.RepositoryIdentity.Device(), config.RepositoryIdentity.Inode()); err != nil {
		return invalidContract(err)
	}
	private := []string{config.ProviderHome, config.ProviderTemp, config.AttemptTokenPath}
	for _, path := range private {
		if !strictDescendant(config.RuntimePath, path) {
			return invalidContract(nil)
		}
	}
	if config.ProviderHome == config.ProviderTemp || config.ProviderHome == config.AttemptTokenPath || config.ProviderTemp == config.AttemptTokenPath {
		return invalidContract(nil)
	}
	if config.AttemptTokenPath != filepath.Join(config.RuntimePath, attemptTokenName) {
		return invalidContract(nil)
	}
	return nil
}

func encodeSelectionCheckpoint(checkpoint selectionCheckpoint) ([]byte, error) {
	if err := validateSelectionCheckpoint(checkpoint); err != nil {
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

func decodeSelectionCheckpoint(encoded []byte) (selectionCheckpoint, error) {
	reader, err := newContractReader(encoded, kindSelection, checkpointLimit)
	if err != nil {
		return selectionCheckpoint{}, err
	}
	formatCode, err := reader.byte()
	if err != nil {
		return selectionCheckpoint{}, err
	}
	format, err := decodeFormat(formatCode)
	if err != nil {
		return selectionCheckpoint{}, err
	}
	baseBytes, err := reader.fixed(format.OIDLength())
	if err != nil {
		return selectionCheckpoint{}, err
	}
	base, err := change.NewObjectID(format, baseBytes)
	if err != nil {
		return selectionCheckpoint{}, invalidContract(err)
	}
	commitmentBytes, err := reader.fixed(32)
	if err != nil {
		return selectionCheckpoint{}, err
	}
	commitment, err := change.ParseCommitment(commitmentBytes)
	if err != nil {
		return selectionCheckpoint{}, invalidContract(err)
	}
	entries, err := reader.uint64()
	if err != nil {
		return selectionCheckpoint{}, err
	}
	blobs, err := reader.uint64()
	if err != nil {
		return selectionCheckpoint{}, err
	}
	device, err := reader.uint64()
	if err != nil {
		return selectionCheckpoint{}, err
	}
	inode, err := reader.uint64()
	if err != nil || !reader.done() {
		return selectionCheckpoint{}, invalidContract(err)
	}
	repository, err := change.NewRepositoryIdentity(device, inode)
	if err != nil {
		return selectionCheckpoint{}, invalidContract(err)
	}
	checkpoint := selectionCheckpoint{Format: format, Base: base, Commitment: commitment, EntryCount: entries, BlobBytes: blobs, Repository: repository}
	if err := validateSelectionCheckpoint(checkpoint); err != nil {
		return selectionCheckpoint{}, err
	}
	return checkpoint, nil
}

func validateSelectionCheckpoint(checkpoint selectionCheckpoint) error {
	if checkpoint.Format.OIDLength() == 0 || checkpoint.Base.Format() != checkpoint.Format || len(checkpoint.Base.Bytes()) != checkpoint.Format.OIDLength() ||
		len(checkpoint.Commitment.Bytes()) != 32 || checkpoint.EntryCount > change.MaxEntryCount || checkpoint.BlobBytes > change.MaxTotalBlobBytes {
		return invalidContract(nil)
	}
	if _, err := change.NewRepositoryIdentity(checkpoint.Repository.Device(), checkpoint.Repository.Inode()); err != nil {
		return invalidContract(err)
	}
	return nil
}

func encodePreparationCheckpoint(checkpoint preparationCheckpoint) ([]byte, error) {
	if _, err := change.NewStageIdentity(checkpoint.Stage.Device(), checkpoint.Stage.Inode()); err != nil {
		return nil, invalidContract(err)
	}
	encoded := appendHeader(nil, kindPreparation)
	encoded = binary.BigEndian.AppendUint64(encoded, checkpoint.Stage.Device())
	encoded = binary.BigEndian.AppendUint64(encoded, checkpoint.Stage.Inode())
	return boundedCheckpoint(encoded)
}

func decodePreparationCheckpoint(encoded []byte) (preparationCheckpoint, error) {
	reader, err := newContractReader(encoded, kindPreparation, checkpointLimit)
	if err != nil {
		return preparationCheckpoint{}, err
	}
	device, err := reader.uint64()
	if err != nil {
		return preparationCheckpoint{}, err
	}
	inode, err := reader.uint64()
	if err != nil || !reader.done() {
		return preparationCheckpoint{}, invalidContract(err)
	}
	stage, err := change.NewStageIdentity(device, inode)
	if err != nil {
		return preparationCheckpoint{}, invalidContract(err)
	}
	return preparationCheckpoint{Stage: stage}, nil
}

func encodePopulationCheckpoint(checkpoint populationCheckpoint) ([]byte, error) {
	if err := validatePopulationCheckpoint(checkpoint); err != nil {
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

func decodePopulationCheckpoint(encoded []byte) (populationCheckpoint, error) {
	reader, err := newContractReader(encoded, kindPopulation, checkpointLimit)
	if err != nil {
		return populationCheckpoint{}, err
	}
	device, err := reader.uint64()
	if err != nil {
		return populationCheckpoint{}, err
	}
	inode, err := reader.uint64()
	if err != nil {
		return populationCheckpoint{}, err
	}
	stage, err := change.NewStageIdentity(device, inode)
	if err != nil {
		return populationCheckpoint{}, invalidContract(err)
	}
	commitmentBytes, err := reader.fixed(32)
	if err != nil {
		return populationCheckpoint{}, err
	}
	commitment, err := change.ParseCommitment(commitmentBytes)
	if err != nil {
		return populationCheckpoint{}, invalidContract(err)
	}
	entries, err := reader.uint64()
	if err != nil {
		return populationCheckpoint{}, err
	}
	blobs, err := reader.uint64()
	if err != nil || !reader.done() {
		return populationCheckpoint{}, invalidContract(err)
	}
	checkpoint := populationCheckpoint{Identity: stage, Commitment: commitment, EntryCount: entries, BlobBytes: blobs}
	if err := validatePopulationCheckpoint(checkpoint); err != nil {
		return populationCheckpoint{}, err
	}
	return checkpoint, nil
}

func validatePopulationCheckpoint(checkpoint populationCheckpoint) error {
	if _, err := change.NewStageIdentity(checkpoint.Identity.Device(), checkpoint.Identity.Inode()); err != nil {
		return invalidContract(err)
	}
	if len(checkpoint.Commitment.Bytes()) != 32 || checkpoint.EntryCount > change.MaxEntryCount || checkpoint.BlobBytes > change.MaxTotalBlobBytes {
		return invalidContract(nil)
	}
	return nil
}

func boundedCheckpoint(encoded []byte) ([]byte, error) {
	if len(encoded) > checkpointLimit {
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
		return format, invalidChangeError(err)
	case 2:
		format, err := change.NewObjectFormat("sha256")
		return format, invalidChangeError(err)
	default:
		return 0, invalidContract(nil)
	}
}

func invalidChangeError(err error) error {
	if err == nil {
		return nil
	}
	return invalidContract(err)
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

func strictDescendant(parent, child string) bool {
	return child != parent && strings.HasPrefix(child, parent+string(filepath.Separator))
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
