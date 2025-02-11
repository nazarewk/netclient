package functions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/devilcove/httpclient"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gravitl/netclient/auth"
	"github.com/gravitl/netclient/config"
	"github.com/gravitl/netclient/daemon"
	"github.com/gravitl/netclient/metrics"
	"github.com/gravitl/netclient/ncutils"
	"github.com/gravitl/netmaker/logger"
	"github.com/gravitl/netmaker/models"
	"golang.org/x/exp/slog"
)

const (
	// ACK - acknowledgement signal for MQ
	ACK = 1
	// DONE - done signal for MQ
	DONE = 2
	// CheckInInterval - interval in minutes for mq checkins
	CheckInInterval = 1
)

// Checkin  -- go routine that checks for public or local ip changes, publishes changes
//
//	if there are no updates, simply "pings" the server as a checkin
func Checkin(ctx context.Context, wg *sync.WaitGroup) {
	logger.Log(2, "starting checkin goroutine")
	defer wg.Done()
	ticker := time.NewTicker(time.Minute * CheckInInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Log(0, "checkin routine closed")
			return
		case <-ticker.C:
			if config.CurrServer == "" {
				continue
			}
			if Mqclient == nil || !Mqclient.IsConnectionOpen() {
				logger.Log(0, "MQ client is not connected, using fallback checkin for server", config.CurrServer)
				checkinFallback()
				continue
			}
			checkin()
		}
	}
}

func checkinFallback() {
	// check/update host settings; publish if changed
	if err := UpdateHostSettings(true); err != nil {
		logger.Log(0, "failed to update host settings", err.Error())
		return
	}
	hostUpdateFallback(models.CheckIn, nil)
}

// hostUpdateFallback - used to send host updates to server when there is a mq connection failure
func hostUpdateFallback(action models.HostMqAction, node *models.Node) error {
	if node == nil {
		node = &models.Node{}
	}
	server := config.GetServer(config.CurrServer)
	if server == nil {
		return errors.New("server config not found")
	}
	host := config.Netclient()
	if host == nil {
		return fmt.Errorf("no configured host found")
	}
	token, err := auth.Authenticate(server, host)
	if err != nil {
		return err
	}
	endpoint := httpclient.JSONEndpoint[models.SuccessResponse, models.ErrorResponse]{
		URL:           "https://" + server.API,
		Route:         fmt.Sprintf("/api/v1/fallback/host/%s", url.QueryEscape(host.ID.String())),
		Method:        http.MethodPut,
		Data:          models.HostUpdate{Host: host.Host, Action: action, Node: *node},
		Authorization: "Bearer " + token,
		ErrorResponse: models.ErrorResponse{},
	}
	_, errData, err := endpoint.GetJSON(models.SuccessResponse{}, models.ErrorResponse{})
	if err != nil {
		if errors.Is(err, httpclient.ErrStatus) {
			slog.Error("error sending host update to server", "code", strconv.Itoa(errData.Code), "error", errData.Message)
		}
		return err
	}
	return nil
}

func checkin() {
	// check/update host settings; publish if changed
	if err := UpdateHostSettings(false); err != nil {
		logger.Log(0, "failed to update host settings", err.Error())
		return
	}
	if err := PublishHostUpdate(config.CurrServer, models.HostMqAction(models.CheckIn)); err != nil {
		logger.Log(0, "error publishing checkin", err.Error())
		return
	}
}

// PublishNodeUpdate -- pushes node to broker
func PublishNodeUpdate(node *config.Node) error {
	server := config.GetServer(node.Server)
	if server == nil || server.Name == "" {
		return errors.New("no server for " + node.Network)
	}
	data, err := json.Marshal(node)
	if err != nil {
		return err
	}
	if err = publish(node.Server, fmt.Sprintf("update/%s/%s", node.Server, node.ID), data, 1); err != nil {
		return err
	}

	logger.Log(0, "network:", node.Network, "sent a node update to server for node", config.Netclient().Name, ", ", node.ID.String())
	return nil
}

// publishPeerSignal - publishes peer signal
func publishPeerSignal(server string, signal models.Signal) error {
	hostCfg := config.Netclient()
	hostUpdate := models.HostUpdate{
		Action: models.SignalHost,
		Host:   hostCfg.Host,
		Signal: signal,
	}
	data, err := json.Marshal(hostUpdate)
	if err != nil {
		return err
	}
	if err = publish(server, fmt.Sprintf("host/serverupdate/%s/%s", server, hostCfg.ID.String()), data, 1); err != nil {
		return err
	}
	return nil
}

// PublishHostUpdate - publishes host updates to server
func PublishHostUpdate(server string, hostAction models.HostMqAction) error {
	hostCfg := config.Netclient()
	hostUpdate := models.HostUpdate{
		Action: hostAction,
		Host:   hostCfg.Host,
	}
	data, err := json.Marshal(hostUpdate)
	if err != nil {
		return err
	}
	if err = publish(server, fmt.Sprintf("host/serverupdate/%s/%s", server, hostCfg.ID.String()), data, 1); err != nil {
		return err
	}
	return nil
}

// publishMetrics - publishes the metrics of a given nodecfg
func publishMetrics(node *config.Node, fallback bool) {
	server := config.GetServer(node.Server)
	if server == nil {
		return
	}
	token, err := auth.Authenticate(server, config.Netclient())
	if err != nil {
		logger.Log(1, "failed to authenticate when publishing metrics", err.Error())
		return
	}
	url := fmt.Sprintf("https://%s/api/nodes/%s/%s", server.API, node.Network, node.ID)
	endpoint := httpclient.JSONEndpoint[models.NodeGet, models.ErrorResponse]{
		URL:           url,
		Method:        http.MethodGet,
		Authorization: "Bearer " + token,
		Data:          nil,
		Response:      models.NodeGet{},
		ErrorResponse: models.ErrorResponse{},
	}
	response, errData, err := endpoint.GetJSON(models.NodeGet{}, models.ErrorResponse{})
	if err != nil {
		if errors.Is(err, httpclient.ErrStatus) {
			logger.Log(0, "status error calling ", endpoint.URL, errData.Message)
			return
		}
		logger.Log(1, "failed to read from server during metrics publish", err.Error())
		return
	}
	nodeGET := response

	metrics, err := metrics.Collect(nodeGET.Node.Network, nodeGET.PeerIDs)
	if err != nil {
		logger.Log(0, "failed metric collection for node", config.Netclient().Name, err.Error())
		return
	}
	metrics.Network = node.Network
	metrics.NodeName = config.Netclient().Name
	metrics.NodeID = node.ID.String()
	data, err := json.Marshal(metrics)
	if err != nil {
		logger.Log(0, "something went wrong when marshalling metrics data for node", config.Netclient().Name, err.Error())
		return
	}
	if fallback {
		hostUpdateFallback(models.UpdateMetrics, &nodeGET.Node)
		return
	}
	if err = publish(node.Server, fmt.Sprintf("metrics/%s/%s", node.Server, node.ID), data, 1); err != nil {
		logger.Log(0, "error occurred during publishing of metrics on node", config.Netclient().Name, err.Error())

	} else {
		hostUpdateFallback(models.UpdateMetrics, &nodeGET.Node)
	}

}

func publish(serverName, dest string, msg []byte, qos byte) error {
	// setup the keys
	server := config.GetServer(serverName)
	if server == nil {
		return errors.New("server config is nil")
	}
	serverPubKey, err := ncutils.ConvertBytesToKey(server.TrafficKey)
	if err != nil {
		return err
	}
	privateKey, err := ncutils.ConvertBytesToKey(config.Netclient().TrafficKeyPrivate)
	if err != nil {
		return err
	}
	encrypted, err := Chunk(msg, serverPubKey, privateKey)
	if err != nil {
		return err
	}
	if token := Mqclient.Publish(dest, qos, false, encrypted); !token.WaitTimeout(30*time.Second) || token.Error() != nil {
		logger.Log(0, "could not connect to broker at "+serverName)
		var err error
		if token.Error() == nil {
			err = errors.New("connection timeout")
		} else {
			err = token.Error()
		}
		slog.Error("could not connect to broker at", "server", serverName, "error", err)
		if err != nil {
			return err
		}
	}
	return nil
}

// UpdateHostSettings - checks local host settings, if different, mod config and publish
func UpdateHostSettings(fallback bool) error {
	_ = config.ReadNodeConfig()
	_ = config.ReadServerConf()
	logger.Log(3, "checkin with server(s)")
	var (
		err           error
		publishMsg    bool
		restartDaemon bool
	)

	server := config.GetServer(config.CurrServer)
	if server == nil {
		return errors.New("server config is nil")
	}
	if !config.Netclient().IsStatic {
		if config.Netclient().EndpointIP == nil {
			config.Netclient().EndpointIP = config.HostPublicIP
		} else {
			if config.HostPublicIP != nil && !config.HostPublicIP.IsUnspecified() && !config.Netclient().EndpointIP.Equal(config.HostPublicIP) {
				logger.Log(0, "endpoint has changed from", config.Netclient().EndpointIP.String(), "to", config.HostPublicIP.String())
				config.Netclient().EndpointIP = config.HostPublicIP
				publishMsg = true
			}
		}
	}
	if config.WgPublicListenPort != 0 && config.Netclient().WgPublicListenPort != config.WgPublicListenPort {
		config.Netclient().WgPublicListenPort = config.WgPublicListenPort
		publishMsg = true
	}

	if config.HostNatType != "" && config.Netclient().NatType != config.HostNatType {
		config.Netclient().NatType = config.HostNatType
		publishMsg = true
	}
	if server.IsPro {
		serverNodes := config.GetNodes()
		for _, node := range serverNodes {
			node := node
			if node.Connected {
				slog.Debug("collecting metrics for", "network", node.Network)
				go publishMetrics(&node, fallback)
			}
		}
	}

	ip, err := getInterfaces()
	if err != nil {
		logger.Log(0, "failed to retrieve local interfaces during check-in", err.Error())
	} else {
		if ip != nil {
			if len(*ip) != len(config.Netclient().Interfaces) {
				config.Netclient().Interfaces = *ip
				publishMsg = true
			}
		}
	}
	defaultInterface, err := getDefaultInterface()
	if err != nil {
		logger.Log(0, "default gateway not found", err.Error())
	} else {
		if defaultInterface != config.Netclient().DefaultInterface &&
			defaultInterface != ncutils.GetInterfaceName() {
			publishMsg = true
			config.Netclient().DefaultInterface = defaultInterface
			logger.Log(0, "default interface has changed to", defaultInterface)
		}
	}
	if config.FirewallHasChanged() {
		config.SetFirewall()
		publishMsg = true
	}
	if publishMsg {
		if err := config.WriteNetclientConfig(); err != nil {
			return err
		}
		slog.Info("publishing host update for endpoint changes")
		if fallback {
			hostUpdateFallback(models.UpdateHost, nil)
		} else {
			if err := PublishHostUpdate(config.CurrServer, models.UpdateHost); err != nil {
				logger.Log(0, "could not publish endpoint change", err.Error())
			}
		}
	}
	if restartDaemon {
		if err := daemon.Restart(); err != nil {
			slog.Error("failed to restart daemon", "error", err)
		}
	}

	return err
}

// publishes a message to server to update peers on this peer's behalf
func publishSignal(node *config.Node, signal byte) error {
	if err := publish(node.Server, fmt.Sprintf("signal/%s/%s", node.Server, node.ID), []byte{signal}, 1); err != nil {
		return err
	}
	return nil
}

// publishes a blank message to the topic to clear the unwanted retained message
func clearRetainedMsg(client mqtt.Client, topic string) {
	if token := client.Publish(topic, 0, true, []byte{}); token.Error() != nil {
		logger.Log(0, "failed to clear retained message: ", topic, token.Error().Error())
	}
}
