"""
MQTT client for Home Assistant integration.

Publishes GLM state and subscribes to control commands.
Supports Home Assistant MQTT Discovery for automatic entity creation.
"""
import json
import logging
import threading
import time
from typing import Optional, Callable

import paho.mqtt.client as mqtt

from glm_core import SetVolume, AdjustVolume, SetMute, SetDim, SetPower, QueuedAction

logger = logging.getLogger(__name__)

# Default topic prefix
DEFAULT_TOPIC_PREFIX = "glm"

# Home Assistant MQTT Discovery prefix
HA_DISCOVERY_PREFIX = "homeassistant"


class MqttClient:
    """MQTT client for GLM control and state publishing."""

    def __init__(
        self,
        action_queue,
        glm_controller,
        broker: str = "localhost",
        port: int = 1883,
        username: Optional[str] = None,
        password: Optional[str] = None,
        topic_prefix: str = DEFAULT_TOPIC_PREFIX,
        ha_discovery: bool = True,
    ):
        """
        Initialize MQTT client.

        Args:
            action_queue: Queue for submitting GlmActions
            glm_controller: GlmController instance for state
            broker: MQTT broker hostname
            port: MQTT broker port
            username: Optional username for authentication
            password: Optional password for authentication
            topic_prefix: Prefix for all topics (default: "glm")
            ha_discovery: Enable Home Assistant MQTT Discovery
        """
        self._action_queue = action_queue
        self._glm_controller = glm_controller
        self._broker = broker
        self._port = port
        self._username = username
        self._password = password
        self._topic_prefix = topic_prefix
        self._ha_discovery = ha_discovery
        self._client: Optional[mqtt.Client] = None
        self._connected = False
        self._stop_event = threading.Event()

        # Topics
        self._state_topic = f"{topic_prefix}/state"
        self._availability_topic = f"{topic_prefix}/availability"
        self._cmd_volume_topic = f"{topic_prefix}/set/volume"
        self._cmd_mute_topic = f"{topic_prefix}/set/mute"
        self._cmd_dim_topic = f"{topic_prefix}/set/dim"
        self._cmd_power_topic = f"{topic_prefix}/set/power"

    def _on_connect(self, client, userdata, flags, rc, properties=None):
        """Handle connection to broker."""
        if rc == 0:
            logger.info(f"Connected to MQTT broker {self._broker}:{self._port}")
            self._connected = True

            # Subscribe to command topics
            client.subscribe(self._cmd_volume_topic)
            client.subscribe(self._cmd_mute_topic)
            client.subscribe(self._cmd_dim_topic)
            client.subscribe(self._cmd_power_topic)
            logger.info(f"Subscribed to {self._topic_prefix}/set/+")

            # Publish availability
            client.publish(self._availability_topic, "online", retain=True)

            # Publish HA Discovery configs
            if self._ha_discovery:
                self._publish_ha_discovery()

            # Publish current state
            self._publish_state(self._glm_controller.get_state())
        else:
            logger.error(f"Failed to connect to MQTT broker, rc={rc}")

    def _on_disconnect(self, client, userdata, rc, properties=None):
        """Handle disconnection from broker."""
        self._connected = False
        if rc != 0:
            logger.warning(f"Unexpected MQTT disconnect, rc={rc}")

    def _on_message(self, client, userdata, msg):
        """Handle incoming MQTT messages."""
        topic = msg.topic
        try:
            payload = msg.payload.decode('utf-8').strip()
        except UnicodeDecodeError:
            logger.warning(f"Invalid payload on {topic}")
            return

        logger.debug(f"MQTT received: {topic} = {payload}")

        try:
            if topic == self._cmd_volume_topic:
                # Volume: accept integer 0-127
                value = int(payload)
                self._submit_action(SetVolume(target=value))

            elif topic == self._cmd_mute_topic:
                # Mute: accept ON/OFF/true/false/1/0/TOGGLE
                state = self._parse_bool_or_toggle(payload)
                self._submit_action(SetMute(state=state))

            elif topic == self._cmd_dim_topic:
                # Dim: accept ON/OFF/true/false/1/0/TOGGLE
                state = self._parse_bool_or_toggle(payload)
                self._submit_action(SetDim(state=state))

            elif topic == self._cmd_power_topic:
                # Power: only toggle supported
                self._submit_action(SetPower())

        except (ValueError, TypeError) as e:
            logger.warning(f"Invalid MQTT command on {topic}: {payload} ({e})")

    def _parse_bool_or_toggle(self, payload: str) -> Optional[bool]:
        """Parse payload to bool or None (toggle)."""
        payload_lower = payload.lower()
        if payload_lower in ('on', 'true', '1'):
            return True
        elif payload_lower in ('off', 'false', '0'):
            return False
        elif payload_lower == 'toggle':
            return None
        else:
            raise ValueError(f"Unknown state: {payload}")

    def _submit_action(self, action):
        """Submit an action to the queue."""
        self._action_queue.put(QueuedAction(action=action, timestamp=time.time()))

    def _publish_state(self, state: dict):
        """Publish current state to MQTT."""
        if not self._connected or self._client is None:
            return

        # Convert to HA-friendly format
        payload = json.dumps({
            "volume": state["volume"],
            "mute": "ON" if state["mute"] else "OFF",
            "dim": "ON" if state["dim"] else "OFF",
            "power": "ON" if state["power"] else "OFF",
        })

        self._client.publish(self._state_topic, payload, retain=True)

    def _publish_ha_discovery(self):
        """Publish Home Assistant MQTT Discovery configuration."""
        if self._client is None:
            return

        device_info = {
            "identifiers": ["glm_controller"],
            "name": "Genelec GLM",
            "manufacturer": "Genelec",
            "model": "GLM Controller",
        }

        # Volume as number entity
        volume_config = {
            "name": "GLM Volume",
            "unique_id": "glm_volume",
            "command_topic": self._cmd_volume_topic,
            "state_topic": self._state_topic,
            "value_template": "{{ value_json.volume }}",
            "min": 0,
            "max": 127,
            "step": 1,
            "icon": "mdi:volume-high",
            "availability_topic": self._availability_topic,
            "device": device_info,
        }
        self._client.publish(
            f"{HA_DISCOVERY_PREFIX}/number/glm_volume/config",
            json.dumps(volume_config),
            retain=True
        )

        # Mute as switch entity
        mute_config = {
            "name": "GLM Mute",
            "unique_id": "glm_mute",
            "command_topic": self._cmd_mute_topic,
            "state_topic": self._state_topic,
            "value_template": "{{ value_json.mute }}",
            "payload_on": "ON",
            "payload_off": "OFF",
            "icon": "mdi:volume-mute",
            "availability_topic": self._availability_topic,
            "device": device_info,
        }
        self._client.publish(
            f"{HA_DISCOVERY_PREFIX}/switch/glm_mute/config",
            json.dumps(mute_config),
            retain=True
        )

        # Dim as switch entity
        dim_config = {
            "name": "GLM Dim",
            "unique_id": "glm_dim",
            "command_topic": self._cmd_dim_topic,
            "state_topic": self._state_topic,
            "value_template": "{{ value_json.dim }}",
            "payload_on": "ON",
            "payload_off": "OFF",
            "icon": "mdi:brightness-6",
            "availability_topic": self._availability_topic,
            "device": device_info,
        }
        self._client.publish(
            f"{HA_DISCOVERY_PREFIX}/switch/glm_dim/config",
            json.dumps(dim_config),
            retain=True
        )

        # Power as switch entity
        power_config = {
            "name": "GLM Power",
            "unique_id": "glm_power",
            "command_topic": self._cmd_power_topic,
            "state_topic": self._state_topic,
            "value_template": "{{ value_json.power }}",
            "payload_on": "ON",
            "payload_off": "OFF",
            "icon": "mdi:power",
            "availability_topic": self._availability_topic,
            "device": device_info,
        }
        self._client.publish(
            f"{HA_DISCOVERY_PREFIX}/switch/glm_power/config",
            json.dumps(power_config),
            retain=True
        )

        logger.info("Published Home Assistant MQTT Discovery configs")

    def on_state_change(self, state: dict):
        """Callback for GlmController state changes."""
        self._publish_state(state)

    def start(self):
        """Start the MQTT client."""
        self._client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2)

        if self._username:
            self._client.username_pw_set(self._username, self._password)

        # Set last will for availability
        self._client.will_set(self._availability_topic, "offline", retain=True)

        # Set callbacks
        self._client.on_connect = self._on_connect
        self._client.on_disconnect = self._on_disconnect
        self._client.on_message = self._on_message

        # Connect
        try:
            self._client.connect(self._broker, self._port, keepalive=60)
        except Exception as e:
            logger.error(f"Failed to connect to MQTT broker: {e}")
            return

        # Start network loop in background thread
        self._client.loop_start()
        logger.info(f"MQTT client started, connecting to {self._broker}:{self._port}")

        # Register state callback
        self._glm_controller.add_state_callback(self.on_state_change)

    def stop(self):
        """Stop the MQTT client."""
        if self._client:
            # Unregister callback
            self._glm_controller.remove_state_callback(self.on_state_change)

            # Publish offline status
            if self._connected:
                self._client.publish(self._availability_topic, "offline", retain=True)

            self._client.loop_stop()
            self._client.disconnect()
            logger.info("MQTT client stopped")


def start_mqtt_client(
    action_queue,
    glm_controller,
    broker: str,
    port: int = 1883,
    username: Optional[str] = None,
    password: Optional[str] = None,
    topic_prefix: str = DEFAULT_TOPIC_PREFIX,
    ha_discovery: bool = True,
) -> MqttClient:
    """
    Start MQTT client for Home Assistant integration.

    Args:
        action_queue: Queue for submitting GlmActions
        glm_controller: GlmController instance
        broker: MQTT broker hostname
        port: MQTT broker port
        username: Optional username
        password: Optional password
        topic_prefix: Topic prefix (default: "glm")
        ha_discovery: Enable HA MQTT Discovery

    Returns:
        MqttClient instance
    """
    client = MqttClient(
        action_queue=action_queue,
        glm_controller=glm_controller,
        broker=broker,
        port=port,
        username=username,
        password=password,
        topic_prefix=topic_prefix,
        ha_discovery=ha_discovery,
    )
    client.start()
    return client
