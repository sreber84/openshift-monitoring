package checks

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const (
	daemonDNSEndpoint = "daemon.ose-mon-a.endpoints.cluster.local"
	daemonDNSServiceA = "daemon.ose-mon-a.svc.cluster.local"
	daemonDNSServiceB = "daemon.ose-mon-b.svc.cluster.local"
	daemonDNSServiceC = "daemon.ose-mon-c.svc.cluster.local"
	daemonDNSPod      = "daemon"
	kubernetesIP      = "172.30.0.10"
)

var num = regexp.MustCompile(`\d+(?:\.\d+)?`)

func CheckExternalSystem(url string) error {
	if err := checkHttp(url); err != nil {
		msg := "Call to " + url + " failed"
		log.Println(msg)
		return errors.New(msg)
	}

	return nil
}

func CheckChrony() error {
	log.Println("Checking output of 'chronyc tracking'")

	out, err := exec.Command("bash", "-c", "chronyc tracking").Output()
	if err != nil {
		msg := "Could not check chrony status: " + err.Error()
		log.Println(msg)
		return errors.New(msg)
	}

	offset, err := parseChronyOffset(string(out))

	if offset < -0.1 || offset > 0.1 { // 100 Millisekunden
		return errors.New("Time is not correct on the server or chrony is not running")
	} else {
		return nil
	}
}

func parseChronyOffset(out string) (float64, error) {
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "Last offset") {
			// Example output
			// Reference ID    : 0A7CD814 (some-ntp-server)
			// Stratum         : 2
			// Ref time (UTC)  : Thu May 31 13:41:40 2018
			// System time     : 0.000037743 seconds fast of NTP time
			// Last offset     : +0.000061081 seconds <--- SECONDS
			// RMS offset      : 0.000333012 seconds
			// Frequency       : 6.629 ppm fast
			// Residual freq   : +0.004 ppm
			// Skew            : 0.140 ppm
			// Root delay      : 0.002649408 seconds
			// Root dispersion : 0.000559144 seconds
			// Update interval : 517.4 seconds
			// Leap status     : Normal
			rgx := regexp.MustCompile("(.*offset\\s+:\\s+)(.*?)\\s+seconds")
			offset := rgx.FindStringSubmatch(line)

			log.Println("Found chrony offset:", offset[2])
			out, err := strconv.ParseFloat(offset[2], 64)
			if err != nil {
				return -1000, fmt.Errorf("couldn't parse chrony offset. Value was %v", offset[2])
			}
			return out, nil
		}
	}
	return -1000, fmt.Errorf("couldn't parse chrony offset. Offset line was not found.")
}

func CheckNtpd() error {
	log.Println("Checking output of 'ntpq -c rv 0 offset'")

	out, err := exec.Command("bash", "-c", "ntpq -c rv 0 offset").Output()
	if err != nil {
		msg := "Could not check ntpd status: " + err.Error()
		log.Println(msg)
		return errors.New(msg)
	}

	offset, err := parseNTPOffsetFromNTPD(string(out))

	if offset < -100 || offset > 100 {
		return errors.New("Time is not correct on the server or ntpd is not running")
	} else {
		return nil
	}
}

func parseNTPOffsetFromNTPD(out string) (float64, error) {
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "offset") {
			// Example output
			// mintc=3, offset=0.400, frequency=-4.546, sys_jitter=1.015,
			// tc=10, mintc=3, offset=-0.648, frequency=3.934, sys_jitter=0.253,
			rgx := regexp.MustCompile("(.*offset=)(.*?),")
			offset := rgx.FindStringSubmatch(l)

			log.Println("Found ntpd offset:", offset[2])
			out, err := strconv.ParseFloat(offset[2], 64)
			if err != nil {
				return -1000, fmt.Errorf("couldn't parse ntp offset. Value was %v", offset[2])
			}
			return out, nil
		}
	}
	return -1000, fmt.Errorf("couldn't parse ntp offset. Offset line was not found.")
}

func getIpsForName(n string) []net.IP {
	ips, err := net.LookupIP(n)
	if err != nil {
		log.Println("failed to lookup ip for name ", n)
		return nil
	}
	return ips
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func checkHttp(toCall string) error {
	log.Println("Checking access to:", toCall)
	if strings.HasPrefix(toCall, "https") {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		client := &http.Client{Transport: tr}
		resp, err := client.Get(toCall)
		if err != nil {
			log.Println("error in http check: ", err)
			return err
		} else {
			resp.Body.Close()
			return nil
		}
	} else {
		resp, err := http.Get(toCall)
		if err != nil {
			log.Println("error in http check: ", err)
			return err
		} else {
			resp.Body.Close()
			return nil
		}
	}
}

func getEndpoint(slow bool) string {
	if slow {
		return "slow"
	} else {
		return "fast"
	}
}

// isVgSizeOk returns true if vgs output in stdOut indicates that the volume
// group free space is equal or above the percentage treshold okSize, which is
// expected to be in the range [0, 100].
func isVgSizeOk(stdOut string, okSize int) bool {
	// Example
	// 5.37 26.84 vg_fast_registry
	// 5.37 26.84 vg_slow
	nums := num.FindAllString(stdOut, 2)

	if len(nums) != 2 {
		log.Println("Unable to parse vgs output:", stdOut)
		return false
	}

	free, err := strconv.ParseFloat(nums[0], 64)
	if err != nil {
		log.Println("Unable to parse first digit of output", stdOut)
		return false
	}
	size, err := strconv.ParseFloat(nums[1], 64)
	if err != nil {
		log.Println("Unable to parse second digit of output", stdOut)
		return false
	}

	// calculate usage
	if 100/size*free < float64(okSize) {
		msg := fmt.Sprintf("VG free size is below treshold. Size: %v, free: %v, treshold: %v %%", size, free, okSize)
		log.Println(msg)
		return false
	}

	return true
}

// isLvsSizeOk returns true if lvs output in stdOut indicates that the logical
// volume percentage full for data and metadata are both below the threshold
// okSize, which is expected to be in the range [0, 100].
func isLvsSizeOk(stdOut string, okSize int) bool {
	// Examples
	// 42.10  8.86   docker-pool
	// 13.63  8.93   lv_fast_registry_pool
	checksOk := 0
	for _, nr := range num.FindAllString(stdOut, -1) {
		i, err := strconv.ParseFloat(nr, 64)
		if err != nil {
			log.Print("Unable to parse int:", nr)
			return false
		}

		if i < float64(okSize) {
			checksOk++
		} else {
			log.Println("LVM pool size exceeded okSize:", i)
		}
	}

	return checksOk == 2
}
