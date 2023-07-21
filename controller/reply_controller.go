package controller

import (
	"fmt"
	"github.com/ahmetson/common-lib/data_type/key_value"
	"github.com/ahmetson/service-lib/configuration"
	"github.com/ahmetson/service-lib/remote"

	"github.com/ahmetson/service-lib/communication/command"
	"github.com/ahmetson/service-lib/communication/message"
	"github.com/ahmetson/service-lib/log"

	zmq "github.com/pebbe/zmq4"
)

// Controller is the socket wrapper for the service.
type Controller struct {
	config             *configuration.Controller
	socket             *zmq.Socket
	logger             *log.Logger
	controllerType     configuration.Type
	routes             command.Routes
	requiredExtensions []string
	extensionConfigs   key_value.KeyValue
	extensions         remote.Clients
}

// NewReplier creates a new synchronous Reply controller.
func NewReplier(logger *log.Logger) (*Controller, error) {
	controllerLogger := logger.Child("controller", "type", configuration.ReplierType)

	// Socket to talk to clients
	socket, err := zmq.NewSocket(zmq.REP)
	if err != nil {
		return nil, fmt.Errorf("zmq.NewSocket: %w", err)
	}

	return &Controller{
		socket:             socket,
		logger:             controllerLogger,
		controllerType:     configuration.ReplierType,
		routes:             command.NewRoutes(),
		requiredExtensions: make([]string, 0),
		extensionConfigs:   key_value.Empty(),
		extensions:         key_value.Empty(),
	}, nil
}

// AddConfig adds the parameters of the controller from the configuration
func (c *Controller) AddConfig(controller *configuration.Controller) {
	c.config = controller
}

// AddExtensionConfig adds the configuration of the extension that the controller depends on
func (c *Controller) AddExtensionConfig(extension *configuration.Extension) {
	c.extensionConfigs.Set(extension.Name, extension)
}

// RequireExtension marks the extensions that this controller depends on.
// Before running, the required extension should be added from the configuration.
// Otherwise, controller won't run.
func (c *Controller) RequireExtension(name string) {
	c.requiredExtensions = append(c.requiredExtensions, name)
}

// RequiredExtensions returns the list of extension names required by this controller
func (c *Controller) RequiredExtensions() []string {
	return c.requiredExtensions
}

func (c *Controller) isReply() bool {
	return c.controllerType == configuration.ReplierType
}

// reply sends to the caller the message.
//
// If controller doesn't support replying (for example PULL controller)
// then it returns success.
func (c *Controller) reply(message message.Reply) error {
	if !c.isReply() {
		return nil
	}

	reply, _ := message.String()
	if _, err := c.socket.SendMessage(reply); err != nil {
		return fmt.Errorf("recv error replying error %w" + err.Error())
	}

	return nil
}

// Calls controller.reply() with the error message.
func (c *Controller) replyError(err error) error {
	return c.reply(message.Fail(err.Error()))
}

// AddRoute adds a command along with its handler to this controller
func (c *Controller) AddRoute(route *command.Route) error {
	if c.routes.Exist(route.Command) {
		return nil
	}

	err := c.routes.Add(route.Command, route)
	if err != nil {
		return fmt.Errorf("failed to add a route: %w", err)
	}

	return nil
}

// extensionsAdded checks that the required extensions are added into the controller.
func (c *Controller) extensionsAdded() error {
	for _, name := range c.requiredExtensions {
		if err := c.extensionConfigs.Exist(name); err != nil {
			return fmt.Errorf("required '%s' extension. but it wasn't added to the controller (does it exist in seascape.yml?)", name)
		}
	}

	return nil
}

func (c *Controller) ControllerType() configuration.Type {
	return c.controllerType
}

// initExtensionClients will set up the extension clients for this controller.
// it will be called by c.Run(), automatically.
//
// The reason of call of this function by c.Run() is due to the thread-safety.
//
// The controller is intended to be called as the goroutine. And if the sockets
// are not initiated within the same goroutine, then zeromq doesn't guarantee the socket work
// as it's intended.
func (c *Controller) initExtensionClients() error {
	for _, extensionInterface := range c.extensionConfigs {
		extensionConfig := extensionInterface.(*configuration.Extension)
		extension, err := remote.NewReq(extensionConfig.Name, extensionConfig.Port, c.logger)
		if err != nil {
			return fmt.Errorf("failed to create a request client: %w", err)
		}
		c.extensions.Set(extensionConfig.Name, extension)
	}

	return nil
}

// Run the controller.
//
// It will bind itself to the socket endpoint and waits for the message.Request.
// If message.Request.Command is defined in the handlers, then executes it.
//
// Valid call:
//
//		reply, _ := controller.NewReply(service, reply)
//	 	go reply.Run(handlers, database) // or reply.Run(handlers)
//
// The parameters are the list of parameters that are passed to the command handlers
func (c *Controller) Run() error {
	if err := c.extensionsAdded(); err != nil {
		return fmt.Errorf("extensionsAdded: %w", err)
	}
	if err := c.initExtensionClients(); err != nil {
		return fmt.Errorf("initExtensionClients: %w", err)
	}
	// if secure and not inproc
	// then we add the domain name of controller to the security layer
	//
	// then any whitelisting users will be sent there.
	c.logger.Warn("config.Instances[0] is hardcoded. Create multiple instances")
	c.logger.Warn("todo", "todo 1", "make sure that all ports are different")
	if err := c.socket.Bind(controllerUrl(c.config.Instances[0].Port)); err != nil {
		return fmt.Errorf("socket.bind on tcp protocol for %s at url %d: %w", c.config.Name, c.config.Instances[0].Port, err)
	}

	for {
		msgRaw, metadata, err := c.socket.RecvMessageWithMetadata(0, "pub_key")
		if err != nil {
			newErr := fmt.Errorf("socket.recvMessageWithMetadata: %w", err)
			if err := c.replyError(newErr); err != nil {
				return err
			}
			return newErr
		}

		// All request types derive from the basic request.
		// We first attempt to parse basic request from the raw message
		request, err := message.ParseRequest(msgRaw)
		if err != nil {
			newErr := fmt.Errorf("message.ParseRequest: %w", err)
			if err := c.replyError(newErr); err != nil {
				return err
			}
			continue
		}
		pubKey, ok := metadata["pub_key"]
		if ok {
			request.SetPublicKey(pubKey)
		}

		var reply message.Reply
		var routeInterface interface{}

		if c.routes.Exist(request.Command) {
			routeInterface, err = c.routes.Get(request.Command)
		} else if c.routes.Exist(command.Any) {
			routeInterface, err = c.routes.Get(command.Any)
		} else {
			err = fmt.Errorf("handler not found for command: %s", request.Command)
		}

		if err != nil {
			reply = message.Fail("route get " + request.Command + " failed: " + err.Error())
		} else {
			route := routeInterface.(*command.Route)
			// for puller's it returns an error that occurred on the blockchain.
			reply = route.Handle(request, c.logger, c.extensions)
		}

		if err := c.reply(reply); err != nil {
			return err
		}
		if !reply.IsOK() && !c.isReply() {
			c.logger.Warn("handler replied an error", "command", request.Command, "request parameters", request.Parameters, "error message", reply.Message)
		}
	}
}

// controllerUrl creates url of the controller on tcp protocol
func controllerUrl(port uint64) string {
	url := fmt.Sprintf("tcp://*:%d", port)
	return url
}
