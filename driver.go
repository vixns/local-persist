package main

import (
    "fmt"
    "sync"
    "os"
    "strconv"
    "io/ioutil"
    "path"
    "encoding/json"

    "github.com/docker/go-plugins-helpers/volume"
    "github.com/docker/engine-api/client"
    "github.com/docker/engine-api/types"
    "github.com/fatih/color"
    "golang.org/x/net/context"
)

var (
    // red = color.New(color.FgRed).SprintfFunc()
    // green = color.New(color.FgGreen).SprintfFunc()
    yellow = color.New(color.FgYellow).SprintfFunc()
    cyan = color.New(color.FgCyan).SprintfFunc()
    blue = color.New(color.FgBlue).SprintfFunc()
    magenta = color.New(color.FgMagenta).SprintfFunc()
    white = color.New(color.FgWhite).SprintfFunc()
)

type localPersistDriver struct {
    volumes    map[string]string
    mutex      *sync.Mutex
    debug      bool
    name       string
    baseDir    string
    stateDir   string
}

type saveData struct {
    State map[string]string `json:"state"`
}

func newLocalPersistDriver(name string, baseDir string, stateDir string, debug bool) localPersistDriver {
    if(debug) {
        fmt.Printf(white("%-18s", "Starting... "))
    }
    driver := localPersistDriver{
        volumes  : map[string]string{},
        mutex    : &sync.Mutex{},
        debug    : debug,
        name     : name,
        baseDir  : baseDir,
        stateDir : stateDir,
    }

    os.Mkdir(stateDir, 0700)

    _, driver.volumes = driver.findExistingVolumesFromStateFile()
    if(driver.debug) {
        fmt.Printf("Found %s volumes on startup\n", yellow(strconv.Itoa(len(driver.volumes))))
    }

    return driver
}

func (driver localPersistDriver) Get(req volume.Request) volume.Response {
    if(driver.debug) {
        fmt.Print(white("%-18s", "Get Called... "))
    }
    if driver.exists(req.Name) {
        if(driver.debug) {
            fmt.Printf("Found %s\n", cyan(req.Name))
        }
        return volume.Response{
            Volume: driver.volume(req.Name),
        }
    }

    if(driver.debug) {
       fmt.Printf("Couldn't find %s\n", cyan(req.Name))
    }
    return volume.Response{
        Err: fmt.Sprintf("No volume found with the name %s", cyan(req.Name)),
    }
}

func (driver localPersistDriver) List(req volume.Request) volume.Response {
    if(driver.debug) {
        fmt.Print(white("%-18s", "List Called... "))
    }
    var volumes []*volume.Volume
    for name, _ := range driver.volumes {
        volumes = append(volumes, driver.volume(name))
    }

    if(driver.debug) {
        fmt.Printf("Found %s volumes\n", yellow(strconv.Itoa(len(volumes))))
    }
    return volume.Response{
        Volumes: volumes,
    }
}

func (driver localPersistDriver) Create(req volume.Request) volume.Response {
    if(driver.debug) {
        fmt.Print(white("%-18s", "Create Called... "))
    }

    mountpoint := req.Options["mountpoint"]
    if mountpoint == "" {
        fmt.Printf("No %s option provided\n", blue("mountpoint"))
        return volume.Response{ Err: fmt.Sprintf("The `mountpoint` option is required") }
    }
    realMountpoint := path.Join(driver.baseDir, mountpoint)

    driver.mutex.Lock()
    defer driver.mutex.Unlock()

    if driver.exists(req.Name) {
        return volume.Response{ Err: fmt.Sprintf("The volume %s already exists", req.Name) }
    }

    err := os.MkdirAll(realMountpoint, 0755)
    if(driver.debug) {
        fmt.Printf("Ensuring directory %s exists on host...\n", magenta(realMountpoint))
    }
    if err != nil {
        fmt.Printf("%17s Could not create directory %s\n", " ", magenta(realMountpoint))
        return volume.Response{ Err: err.Error() }
    }

    driver.volumes[req.Name] = mountpoint
    e := driver.saveState(driver.volumes)
    if e != nil {
        fmt.Println(e.Error())
    }

    if(driver.debug) {
        fmt.Printf("%17s Created volume %s with mountpoint %s\n", " ", cyan(req.Name), magenta(realMountpoint))
    }
    return volume.Response{}
}

func (driver localPersistDriver) Remove(req volume.Request) volume.Response {
    if(driver.debug) {
        fmt.Print(white("%-18s", "Remove Called... "))
    }
    driver.mutex.Lock()
    defer driver.mutex.Unlock()

    delete(driver.volumes, req.Name)

    err := driver.saveState(driver.volumes)
    if err != nil {
        fmt.Println(err.Error())
    }

    if(driver.debug) {
        fmt.Printf("Removed %s\n", cyan(req.Name))
    }

    return volume.Response{}
}

func (driver localPersistDriver) Mount(req volume.Request) volume.Response {
    if(driver.debug) {
        fmt.Print(white("%-18s", "Mount Called... "))

        fmt.Printf("Mounted %s\n", cyan(req.Name))
    }
    return driver.Path(req)
}

func (driver localPersistDriver) Path(req volume.Request) volume.Response {
    if(driver.debug) {
        fmt.Print(white("%-18s", "Path Called... "))

        fmt.Printf("Returned path %s\n", magenta(driver.volumes[req.Name]))
    }
    return volume.Response{ Mountpoint: path.Join(driver.baseDir, driver.volumes[req.Name]) }
}

func (driver localPersistDriver) Unmount(req volume.Request) volume.Response {
    if(driver.debug) {        
        fmt.Print(white("%-18s", "Unmount Called... "))

        fmt.Printf("Unmounted %s\n", cyan(req.Name))
    }

    return driver.Path(req)
}


func (driver localPersistDriver) exists(name string) bool {
    return driver.volumes[name] != ""
}

func (driver localPersistDriver) volume(name string) *volume.Volume {
    return &volume.Volume{
        Name: name,
        Mountpoint: driver.volumes[name],
    }
}

func (driver localPersistDriver) findExistingVolumesFromDockerDaemon() (error, map[string]string) {
    // set up the ability to make API calls to the daemon
    defaultHeaders := map[string]string{"User-Agent": "engine-api-cli-1.0"}
    // need at least Docker 1.9 (API v1.21) for named Volume support
    cli, err := client.NewClient("unix:///var/run/docker.sock", "v1.21", nil, defaultHeaders)
    if err != nil {
        return err, map[string]string{}
    }

    // grab ALL containers...
    options := types.ContainerListOptions{All: true}
    containers, err := cli.ContainerList(context.Background(), options)

    // ...and check to see if any of them belong to this driver and recreate their references
    var volumes = map[string]string{}
    for _, container := range containers {
        info, err := cli.ContainerInspect(context.Background(), container.ID)
        if err != nil {
            // something really weird happened here... PANIC
            panic(err)
        }

        for _, mount := range info.Mounts {
            if mount.Driver == driver.name {
                // @TODO there could be multiple volumes (mounts) with this { name: source } combo, and while that's okay
                // what if they is the same name with a different source? could that happen? if it could,
                // it'd be bad, so maybe we want to panic here?
                volumes[mount.Name] = mount.Source
            }
        }
    }

    if err != nil || len(volumes) == 0 {
        if(driver.debug) {
            fmt.Print("Attempting to load from file state...   ")
        }

        return driver.findExistingVolumesFromStateFile()
    }

    return nil, volumes
}

func (driver localPersistDriver) findExistingVolumesFromStateFile() (error, map[string]string) {
    path := path.Join(driver.stateDir, driver.name + ".json")
    fileData, err := ioutil.ReadFile(path)
    if err != nil {
        return err, map[string]string{}
    }

    var data saveData
    e := json.Unmarshal(fileData, &data)
    if e != nil {
        return e, map[string]string{}
    }

    return nil, data.State
}

func (driver localPersistDriver) saveState(volumes map[string]string) error {
    data := saveData{
        State: volumes,
    }

    fileData, err := json.Marshal(data)
    if err != nil {
        return err
    }

    path := path.Join(driver.stateDir, driver.name + ".json")
    return ioutil.WriteFile(path, fileData, 0600)
}
