package moby

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/xeipuuv/gojsonschema"
	"gopkg.in/yaml.v2"
)

// Moby is the type of a Moby config file
type Moby struct {
	Kernel struct {
		Image   string
		Cmdline string
	}
	Init     []string
	Onboot   []Image
	Services []Image
	Trust    TrustConfig
	Files    []File
}

// TrustConfig is the type of a content trust config
type TrustConfig struct {
	Image []string
	Org   []string
}

// File is the type of a file specification
type File struct {
	Path      string
	Directory bool
	Symlink   string
	Contents  *string
	Source    string
	Optional  bool
	Mode      string
	UID       string `yaml:"uid" json:"uid"`
	GID       string `yaml:"gid" json:"gid"`
}

// Image is the type of an image config
type Image struct {
	Name              string             `yaml:"name" json:"name"`
	Image             string             `yaml:"image" json:"image"`
	Capabilities      *[]string          `yaml:"capabilities" json:"capabilities,omitempty"`
	Ambient           *[]string          `yaml:"ambient" json:"ambient,omitempty"`
	Mounts            *[]specs.Mount     `yaml:"mounts" json:"mounts,omitempty"`
	Binds             *[]string          `yaml:"binds" json:"binds,omitempty"`
	Tmpfs             *[]string          `yaml:"tmpfs" json:"tmpfs,omitempty"`
	Command           *[]string          `yaml:"command" json:"command,omitempty"`
	Env               *[]string          `yaml:"env" json:"env,omitempty"`
	Cwd               string             `yaml:"cwd" json:"cwd"`
	Net               string             `yaml:"net" json:"net"`
	Pid               string             `yaml:"pid" json:"pid"`
	Ipc               string             `yaml:"ipc" json:"ipc"`
	Uts               string             `yaml:"uts" json:"uts"`
	Hostname          string             `yaml:"hostname" json:"hostname"`
	Readonly          *bool              `yaml:"readonly" json:"readonly,omitempty"`
	MaskedPaths       *[]string          `yaml:"maskedPaths" json:"maskedPaths,omitempty"`
	ReadonlyPaths     *[]string          `yaml:"readonlyPaths" json:"readonlyPaths,omitempty"`
	UID               *string            `yaml:"uid" json:"uid,omitempty"`
	GID               *string            `yaml:"gid" json:"gid,omitempty"`
	AdditionalGids    *[]string          `yaml:"additionalGids" json:"additionalGids,omitempty"`
	NoNewPrivileges   *bool              `yaml:"noNewPrivileges" json:"noNewPrivileges,omitempty"`
	OOMScoreAdj       *int               `yaml:"oomScoreAdj" json:"oomScoreAdj,omitempty"`
	DisableOOMKiller  *bool              `yaml:"disableOOMKiller" json:"disableOOMKiller,omitempty"`
	RootfsPropagation *string            `yaml:"rootfsPropagation" json:"rootfsPropagation,omitempty"`
	CgroupsPath       *string            `yaml:"cgroupsPath" json:"cgroupsPath,omitempty"`
	Sysctl            *map[string]string `yaml:"sysctl" json:"sysctl,omitempty"`
	Rlimits           *[]string          `yaml:"rlimits" json:"rlimits,omitempty"`
}

// github.com/go-yaml/yaml treats map keys as interface{} while encoding/json
// requires them to be strings, integers or to implement encoding.TextMarshaler.
// Fix this up by recursively mapping all map[interface{}]interface{} types into
// map[string]interface{}.
// see http://stackoverflow.com/questions/40737122/convert-yaml-to-json-without-struct-golang#answer-40737676
func convert(i interface{}) interface{} {
	switch x := i.(type) {
	case map[interface{}]interface{}:
		m2 := map[string]interface{}{}
		for k, v := range x {
			m2[k.(string)] = convert(v)
		}
		return m2
	case []interface{}:
		for i, v := range x {
			x[i] = convert(v)
		}
	}
	return i
}

// NewConfig parses a config file
func NewConfig(config []byte) (Moby, error) {
	m := Moby{}

	// Parse raw yaml
	var rawYaml interface{}
	err := yaml.Unmarshal(config, &rawYaml)
	if err != nil {
		return m, err
	}

	// Convert to raw JSON
	rawJSON := convert(rawYaml)

	// Validate raw yaml with JSON schema
	schemaLoader := gojsonschema.NewStringLoader(schema)
	documentLoader := gojsonschema.NewGoLoader(rawJSON)
	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return m, err
	}
	if !result.Valid() {
		fmt.Printf("The configuration file is invalid:\n")
		for _, desc := range result.Errors() {
			fmt.Printf("- %s\n", desc)
		}
		return m, fmt.Errorf("invalid configuration file")
	}

	// Parse yaml
	err = yaml.Unmarshal(config, &m)
	if err != nil {
		return m, err
	}

	return m, nil
}

// AppendConfig appends two configs.
func AppendConfig(m0, m1 Moby) Moby {
	moby := m0
	if m1.Kernel.Image != "" {
		moby.Kernel.Image = m1.Kernel.Image
	}
	if m1.Kernel.Cmdline != "" {
		moby.Kernel.Cmdline = m1.Kernel.Cmdline
	}
	moby.Init = append(moby.Init, m1.Init...)
	moby.Onboot = append(moby.Onboot, m1.Onboot...)
	moby.Services = append(moby.Services, m1.Services...)
	moby.Files = append(moby.Files, m1.Files...)
	moby.Trust.Image = append(moby.Trust.Image, m1.Trust.Image...)
	moby.Trust.Org = append(moby.Trust.Org, m1.Trust.Org...)

	return moby
}

// NewImage validates an parses yaml or json for a Image
func NewImage(config []byte) (Image, error) {
	log.Debugf("Reading label config: %s", string(config))

	mi := Image{}

	// Parse raw yaml
	var rawYaml interface{}
	err := yaml.Unmarshal(config, &rawYaml)
	if err != nil {
		return mi, err
	}

	// Convert to raw JSON
	rawJSON := convert(rawYaml)

	// check it is an object not an array
	jsonObject, ok := rawJSON.(map[string]interface{})
	if !ok {
		return mi, fmt.Errorf("JSON is an array not an object: %s", string(config))
	}

	// add a dummy name and image to pass validation
	var dummyName interface{}
	var dummyImage interface{}
	dummyName = "dummyname"
	dummyImage = "dummyimage"
	jsonObject["name"] = dummyName
	jsonObject["image"] = dummyImage

	// Validate it as {"services": [config]}
	var services [1]interface{}
	services[0] = rawJSON
	serviceJSON := map[string]interface{}{"services": services}

	// Validate serviceJSON with JSON schema
	schemaLoader := gojsonschema.NewStringLoader(schema)
	documentLoader := gojsonschema.NewGoLoader(serviceJSON)
	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return mi, err
	}
	if !result.Valid() {
		fmt.Printf("The org.mobyproject.config label is invalid:\n")
		for _, desc := range result.Errors() {
			fmt.Printf("- %s\n", desc)
		}
		return mi, fmt.Errorf("invalid configuration label")
	}

	// Parse yaml
	err = yaml.Unmarshal(config, &mi)
	if err != nil {
		return mi, err
	}

	if mi.Name != "" {
		return mi, fmt.Errorf("name cannot be set in metadata label")
	}
	if mi.Image != "" {
		return mi, fmt.Errorf("image cannot be set in metadata label")
	}

	return mi, nil
}

// ConfigToOCI converts a config specification to an OCI config file
func ConfigToOCI(image Image, trust bool, idMap map[string]uint32) ([]byte, error) {

	// TODO pass through same docker client to all functions
	cli, err := dockerClient()
	if err != nil {
		return []byte{}, err
	}

	inspect, err := dockerInspectImage(cli, image.Image, trust)
	if err != nil {
		return []byte{}, err
	}

	oci, err := ConfigInspectToOCI(image, inspect, idMap)
	if err != nil {
		return []byte{}, err
	}

	return json.MarshalIndent(oci, "", "    ")
}

func defaultMountpoint(tp string) string {
	switch tp {
	case "proc":
		return "/proc"
	case "devpts":
		return "/dev/pts"
	case "sysfs":
		return "/sys"
	case "cgroup":
		return "/sys/fs/cgroup"
	case "mqueue":
		return "/dev/mqueue"
	default:
		return ""
	}
}

// Sort mounts by number of path components so /dev/pts is listed after /dev
type mlist []specs.Mount

func (m mlist) Len() int {
	return len(m)
}
func (m mlist) Less(i, j int) bool {
	return m.parts(i) < m.parts(j)
}
func (m mlist) Swap(i, j int) {
	m[i], m[j] = m[j], m[i]
}
func (m mlist) parts(i int) int {
	return strings.Count(filepath.Clean(m[i].Destination), string(os.PathSeparator))
}

// assignBool does ordered overrides from JSON bool pointers
func assignBool(v1, v2 *bool) bool {
	if v2 != nil {
		return *v2
	}
	if v1 != nil {
		return *v1
	}
	return false
}

// assignBoolPtr does ordered overrides from JSON bool pointers
func assignBoolPtr(v1, v2 *bool) *bool {
	if v2 != nil {
		return v2
	}
	if v1 != nil {
		return v1
	}
	return nil
}

// assignIntPtr does ordered overrides from JSON int pointers
func assignIntPtr(v1, v2 *int) *int {
	if v2 != nil {
		return v2
	}
	if v1 != nil {
		return v1
	}
	return nil
}

// assignUint32 does ordered overrides from JSON uint32 pointers
func assignUint32(v1, v2 *uint32) uint32 {
	if v2 != nil {
		return *v2
	}
	if v1 != nil {
		return *v1
	}
	return 0
}

// assignUint32Array does ordered overrides from JSON uint32 array pointers
func assignUint32Array(v1, v2 *[]uint32) []uint32 {
	if v2 != nil {
		return *v2
	}
	if v1 != nil {
		return *v1
	}
	return []uint32{}
}

// assignStrings does ordered overrides from JSON string array pointers
func assignStrings(v1, v2 *[]string) []string {
	if v2 != nil {
		return *v2
	}
	if v1 != nil {
		return *v1
	}
	return []string{}
}

// assignStrings3 does ordered overrides from JSON string array pointers
func assignStrings3(v1 []string, v2, v3 *[]string) []string {
	if v3 != nil {
		return *v3
	}
	if v2 != nil {
		return *v2
	}
	return v1
}

// assignMaps does ordered overrides from JSON string map pointers
func assignMaps(v1, v2 *map[string]string) map[string]string {
	if v2 != nil {
		return *v2
	}
	if v1 != nil {
		return *v1
	}
	return map[string]string{}
}

// assignBinds does ordered overrides from JSON Bind array pointers
func assignBinds(v1, v2 *[]specs.Mount) []specs.Mount {
	if v2 != nil {
		return *v2
	}
	if v1 != nil {
		return *v1
	}
	return []specs.Mount{}
}

// assignString does ordered overrides from JSON string pointers
func assignString(v1, v2 *string) string {
	if v2 != nil {
		return *v2
	}
	if v1 != nil {
		return *v1
	}
	return ""
}

// assignStringEmpty does ordered overrides if strings are empty, for
// values where there is always an explicit override eg "none"
func assignStringEmpty(v1, v2 string) string {
	if v2 != "" {
		return v2
	}
	return v1
}

// assignStringEmpty3 does ordered overrides if strings are empty, for
// values where there is always an explicit override eg "none"
func assignStringEmpty3(v1, v2, v3 string) string {
	if v3 != "" {
		return v3
	}
	if v2 != "" {
		return v2
	}
	return v1
}

// assign StringEmpty4 does ordered overrides if strings are empty, for
// values where there is always an explicit override eg "none"
func assignStringEmpty4(v1, v2, v3, v4 string) string {
	if v4 != "" {
		return v4
	}
	if v3 != "" {
		return v3
	}
	if v2 != "" {
		return v2
	}
	return v1
}

var allCaps = []string{
	"CAP_AUDIT_CONTROL",
	"CAP_AUDIT_READ",
	"CAP_AUDIT_WRITE",
	"CAP_BLOCK_SUSPEND",
	"CAP_CHOWN",
	"CAP_DAC_OVERRIDE",
	"CAP_DAC_READ_SEARCH",
	"CAP_FOWNER",
	"CAP_FSETID",
	"CAP_IPC_LOCK",
	"CAP_IPC_OWNER",
	"CAP_KILL",
	"CAP_LEASE",
	"CAP_LINUX_IMMUTABLE",
	"CAP_MAC_ADMIN",
	"CAP_MAC_OVERRIDE",
	"CAP_MKNOD",
	"CAP_NET_ADMIN",
	"CAP_NET_BIND_SERVICE",
	"CAP_NET_BROADCAST",
	"CAP_NET_RAW",
	"CAP_SETFCAP",
	"CAP_SETGID",
	"CAP_SETPCAP",
	"CAP_SETUID",
	"CAP_SYSLOG",
	"CAP_SYS_ADMIN",
	"CAP_SYS_BOOT",
	"CAP_SYS_CHROOT",
	"CAP_SYS_MODULE",
	"CAP_SYS_NICE",
	"CAP_SYS_PACCT",
	"CAP_SYS_PTRACE",
	"CAP_SYS_RAWIO",
	"CAP_SYS_RESOURCE",
	"CAP_SYS_TIME",
	"CAP_SYS_TTY_CONFIG",
	"CAP_WAKE_ALARM",
}

func idNumeric(id string, idMap map[string]uint32) (uint32, error) {
	if id == "" || id == "root" {
		return 0, nil
	}
	for k, v := range idMap {
		if id == k {
			return v, nil
		}
	}
	v, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("Cannot find or parse id (%s): %v", id, err)
	}
	return uint32(v), nil
}

// ConfigInspectToOCI converts a config and the output of image inspect to an OCI config
func ConfigInspectToOCI(yaml Image, inspect types.ImageInspect, idMap map[string]uint32) (specs.Spec, error) {
	oci := specs.Spec{}

	var inspectConfig container.Config
	if inspect.Config != nil {
		inspectConfig = *inspect.Config
	}

	// look for org.mobyproject.config label
	var label Image
	labelString := inspectConfig.Labels["org.mobyproject.config"]
	if labelString != "" {
		var err error
		label, err = NewImage([]byte(labelString))
		if err != nil {
			return oci, err
		}
	}

	// command, env and cwd can be taken from image, as they are commonly specified in Dockerfile

	// TODO we could handle entrypoint and cmd independently more like Docker
	inspectCommand := append(inspectConfig.Entrypoint, inspect.Config.Cmd...)
	args := assignStrings3(inspectCommand, label.Command, yaml.Command)

	env := assignStrings3(inspectConfig.Env, label.Env, yaml.Env)

	// empty Cwd not allowed in OCI, must be / in that case
	cwd := assignStringEmpty4("/", inspectConfig.WorkingDir, label.Cwd, yaml.Cwd)

	// the other options will never be in the image config, but may be in label or yaml

	readonly := assignBool(label.Readonly, yaml.Readonly)

	// default options match what Docker does
	procOptions := []string{"nosuid", "nodev", "noexec", "relatime"}
	devOptions := []string{"nosuid", "strictatime", "mode=755", "size=65536k"}
	if readonly {
		devOptions = append(devOptions, "ro")
	}
	ptsOptions := []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620"}
	sysOptions := []string{"nosuid", "noexec", "nodev"}
	if readonly {
		sysOptions = append(sysOptions, "ro")
	}
	cgroupOptions := []string{"nosuid", "noexec", "nodev", "relatime", "ro"}
	// note omits "standard" /dev/shm and /dev/mqueue
	mounts := map[string]specs.Mount{
		"/proc":          {Destination: "/proc", Type: "proc", Source: "proc", Options: procOptions},
		"/dev":           {Destination: "/dev", Type: "tmpfs", Source: "tmpfs", Options: devOptions},
		"/dev/pts":       {Destination: "/dev/pts", Type: "devpts", Source: "devpts", Options: ptsOptions},
		"/sys":           {Destination: "/sys", Type: "sysfs", Source: "sysfs", Options: sysOptions},
		"/sys/fs/cgroup": {Destination: "/sys/fs/cgroup", Type: "cgroup", Source: "cgroup", Options: cgroupOptions},
	}
	for _, t := range assignStrings(label.Tmpfs, yaml.Tmpfs) {
		parts := strings.Split(t, ":")
		if len(parts) > 2 {
			return oci, fmt.Errorf("Cannot parse tmpfs, too many ':': %s", t)
		}
		dest := parts[0]
		opts := []string{}
		if len(parts) == 2 {
			opts = strings.Split(parts[1], ",")
		}
		mounts[dest] = specs.Mount{Destination: dest, Type: "tmpfs", Source: "tmpfs", Options: opts}
	}
	for _, b := range assignStrings(label.Binds, yaml.Binds) {
		parts := strings.Split(b, ":")
		if len(parts) < 2 {
			return oci, fmt.Errorf("Cannot parse bind, missing ':': %s", b)
		}
		if len(parts) > 3 {
			return oci, fmt.Errorf("Cannot parse bind, too many ':': %s", b)
		}
		src := parts[0]
		dest := parts[1]
		opts := []string{"rw", "rbind", "rprivate"}
		if len(parts) == 3 {
			opts = append(strings.Split(parts[2], ","), "rbind")
		}
		mounts[dest] = specs.Mount{Destination: dest, Type: "bind", Source: src, Options: opts}
	}
	for _, m := range assignBinds(label.Mounts, yaml.Mounts) {
		tp := m.Type
		src := m.Source
		dest := m.Destination
		opts := m.Options
		if tp == "" {
			switch src {
			case "mqueue", "devpts", "proc", "sysfs", "cgroup":
				tp = src
			}
		}
		if tp == "" && dest == "/dev" {
			tp = "tmpfs"
		}
		if tp == "" {
			return oci, fmt.Errorf("Mount for destination %s is missing type", dest)
		}
		if src == "" {
			// usually sane, eg proc, tmpfs etc
			src = tp
		}
		if dest == "" {
			dest = defaultMountpoint(tp)
		}
		if dest == "" {
			return oci, fmt.Errorf("Mount type %s is missing destination", tp)
		}
		mounts[dest] = specs.Mount{Destination: dest, Type: tp, Source: src, Options: opts}
	}
	mountList := mlist{}
	for _, m := range mounts {
		mountList = append(mountList, m)
	}
	sort.Sort(mountList)

	namespaces := []specs.LinuxNamespace{}
	// to attach to an existing namespace, easiest to bind mount with nsfs in a system container

	// net, ipc and uts namespaces: default to not creating a new namespace (usually host namespace)
	netNS := assignStringEmpty3("root", label.Net, yaml.Net)
	if netNS != "host" && netNS != "root" {
		if netNS == "none" || netNS == "new" {
			netNS = ""
		}
		namespaces = append(namespaces, specs.LinuxNamespace{Type: specs.NetworkNamespace, Path: netNS})
	}
	ipcNS := assignStringEmpty3("root", label.Ipc, yaml.Ipc)
	if ipcNS != "host" && ipcNS != "root" {
		if ipcNS == "new" {
			ipcNS = ""
		}
		namespaces = append(namespaces, specs.LinuxNamespace{Type: specs.IPCNamespace, Path: ipcNS})
	}
	utsNS := assignStringEmpty3("root", label.Uts, yaml.Uts)
	if utsNS != "host" && utsNS != "root" {
		if utsNS == "new" {
			utsNS = ""
		}
		namespaces = append(namespaces, specs.LinuxNamespace{Type: specs.UTSNamespace, Path: utsNS})
	}

	// default to creating a new pid namespace
	pidNS := assignStringEmpty(label.Pid, yaml.Pid)
	if pidNS != "host" && pidNS != "root" {
		if pidNS == "new" {
			pidNS = ""
		}
		namespaces = append(namespaces, specs.LinuxNamespace{Type: specs.PIDNamespace, Path: pidNS})
	}

	// Always create a new mount namespace
	namespaces = append(namespaces, specs.LinuxNamespace{Type: specs.MountNamespace})

	// TODO user, cgroup namespaces

	// Capabilities
	capCheck := map[string]bool{}
	for _, capability := range allCaps {
		capCheck[capability] = true
	}
	boundingSet := map[string]bool{}
	caps := assignStrings(label.Capabilities, yaml.Capabilities)
	if len(caps) == 1 {
		switch cap := strings.ToLower(caps[0]); cap {
		case "none":
			caps = []string{}
		case "all":
			caps = allCaps[:]
		}
	}
	for _, capability := range caps {
		if !capCheck[capability] {
			return oci, fmt.Errorf("unknown capability: %s", capability)
		}
		boundingSet[capability] = true
	}
	ambient := assignStrings(label.Ambient, yaml.Ambient)
	if len(ambient) == 1 {
		switch cap := strings.ToLower(ambient[0]); cap {
		case "none":
			ambient = []string{}
		case "all":
			ambient = allCaps[:]
		}
	}
	for _, capability := range ambient {
		if !capCheck[capability] {
			return oci, fmt.Errorf("unknown capability: %s", capability)
		}
		boundingSet[capability] = true
	}
	bounding := []string{}
	for capability := range boundingSet {
		bounding = append(bounding, capability)
	}

	rlimitsString := assignStrings(label.Rlimits, yaml.Rlimits)
	rlimits := []specs.LinuxRlimit{}
	for _, limitString := range rlimitsString {
		rs := strings.SplitN(limitString, ",", 3)
		var limit string
		var soft, hard uint64
		switch len(rs) {
		case 3:
			origLimit := limit
			limit = strings.ToUpper(strings.TrimSpace(rs[0]))
			if !strings.HasPrefix(limit, "RLIMIT_") {
				limit = "RLIMIT_" + limit
			}
			softString := strings.TrimSpace(rs[1])
			if strings.ToLower(softString) == "unlimited" {
				soft = 18446744073709551615
			} else {
				var err error
				soft, err = strconv.ParseUint(softString, 10, 64)
				if err != nil {
					return oci, fmt.Errorf("Cannot parse %s as uint64: %v", softString, err)
				}
			}
			hardString := strings.TrimSpace(rs[2])
			if strings.ToLower(hardString) == "unlimited" {
				hard = 18446744073709551615
			} else {
				var err error
				hard, err = strconv.ParseUint(hardString, 10, 64)
				if err != nil {
					return oci, fmt.Errorf("Cannot parse %s as uint64: %v", hardString, err)
				}
			}
			switch limit {
			case
				"RLIMIT_CPU",
				"RLIMIT_FSIZE",
				"RLIMIT_DATA",
				"RLIMIT_STACK",
				"RLIMIT_CORE",
				"RLIMIT_RSS",
				"RLIMIT_NPROC",
				"RLIMIT_NOFILE",
				"RLIMIT_MEMLOCK",
				"RLIMIT_AS",
				"RLIMIT_LOCKS",
				"RLIMIT_SIGPENDING",
				"RLIMIT_MSGQUEUE",
				"RLIMIT_NICE",
				"RLIMIT_RTPRIO",
				"RLIMIT_RTTIME":
				rlimits = append(rlimits, specs.LinuxRlimit{Type: limit, Soft: soft, Hard: hard})
			default:
				return oci, fmt.Errorf("Unknown limit: %s", origLimit)
			}
		default:
			return oci, fmt.Errorf("Cannot parse rlimit: %s", rlimitsString)
		}
	}

	// handle mapping of named uid, gid to numbers
	uidString := assignString(label.UID, yaml.UID)
	gidString := assignString(label.GID, yaml.GID)
	agStrings := assignStrings(label.AdditionalGids, yaml.AdditionalGids)
	uid, err := idNumeric(uidString, idMap)
	if err != nil {
		return oci, err
	}
	gid, err := idNumeric(gidString, idMap)
	if err != nil {
		return oci, err
	}
	additionalGroups := []uint32{}
	for _, id := range agStrings {
		ag, err := idNumeric(id, idMap)
		if err != nil {
			return oci, err
		}
		additionalGroups = append(additionalGroups, ag)
	}

	oci.Version = specs.Version

	oci.Platform = specs.Platform{
		OS:   inspect.Os,
		Arch: inspect.Architecture,
	}

	oci.Process = specs.Process{
		Terminal: false,
		//ConsoleSize
		User: specs.User{
			UID:            uid,
			GID:            gid,
			AdditionalGids: additionalGroups,
			// Username (Windows)
		},
		Args: args,
		Env:  env,
		Cwd:  cwd,
		Capabilities: &specs.LinuxCapabilities{
			Bounding:    bounding,
			Effective:   caps,
			Inheritable: bounding,
			Permitted:   bounding,
			Ambient:     ambient,
		},
		Rlimits:         rlimits,
		NoNewPrivileges: assignBool(label.NoNewPrivileges, yaml.NoNewPrivileges),
		// ApparmorProfile
		// TODO FIXME this has moved in runc spec and needs a revendor and update
		//OOMScoreAdj: assignIntPtr(label.OOMScoreAdj, yaml.OOMScoreAdj),
		// SelinuxLabel
	}

	oci.Root = specs.Root{
		Path:     "rootfs",
		Readonly: readonly,
	}

	oci.Hostname = assignStringEmpty(label.Hostname, yaml.Hostname)
	oci.Mounts = mountList

	oci.Linux = &specs.Linux{
		// UIDMappings
		// GIDMappings
		Sysctl: assignMaps(label.Sysctl, yaml.Sysctl),
		Resources: &specs.LinuxResources{
			// Devices
			DisableOOMKiller: assignBoolPtr(label.DisableOOMKiller, yaml.DisableOOMKiller),
			// Memory
			// CPU
			// Pids
			// BlockIO
			// HugepageLimits
			// Network
		},
		CgroupsPath: assignString(label.CgroupsPath, yaml.CgroupsPath),
		Namespaces:  namespaces,
		// Devices
		// Seccomp
		RootfsPropagation: assignString(label.RootfsPropagation, yaml.RootfsPropagation),
		MaskedPaths:       assignStrings(label.MaskedPaths, yaml.MaskedPaths),
		ReadonlyPaths:     assignStrings(label.ReadonlyPaths, yaml.ReadonlyPaths),
		// MountLabel
		// IntelRdt
	}

	return oci, nil
}
