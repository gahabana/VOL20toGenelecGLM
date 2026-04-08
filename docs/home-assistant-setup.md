# Home Assistant Setup

This guide walks through connecting vol20toglm to Home Assistant via MQTT. Once configured, HA auto-discovers four entities: volume (dB slider), mute, dim, and power switches.

## Prerequisites

- Home Assistant instance running on your network
- vol20toglm (Go or Python) running on the GLM machine

## Step 1: Install Mosquitto MQTT Broker

1. In Home Assistant, go to **Settings → Add-ons → Add-on Store**
2. Search for **Mosquitto broker** and click **Install**
3. After installation, click **Start**
4. Enable **Start on boot** and **Watchdog**

> **Note:** You can use any MQTT broker (not just Mosquitto), but the Mosquitto add-on is the simplest path for HA.

## Step 2: Create an MQTT User

The broker needs credentials for vol20toglm to connect.

1. Go to **Settings → People → Users**
2. Click **Add User**
3. Create a user for the bridge (e.g., username: `mqtt_bridge`, set a password)
4. This can be a regular user — it does not need admin privileges

> **Tip:** Some users prefer a dedicated "local only" user for MQTT devices to keep it separate from human accounts.

## Step 3: Enable MQTT Integration

1. Go to **Settings → Devices & Services → Add Integration**
2. Search for **MQTT** and select it
3. If using the Mosquitto add-on, HA usually auto-discovers it — click **Configure** and accept defaults
4. If configuring manually, enter the broker address (usually `localhost` or `127.0.0.1` if Mosquitto runs on the same HA machine)

## Step 4: Configure vol20toglm

Add the MQTT flags when launching vol20toglm, pointing to your HA instance:

**Go:**
```cmd
vol20toglm.exe --mqtt_broker 192.168.0.100 --mqtt_user mqtt_bridge --mqtt_pass your_password
```

**Python:**
```cmd
python bridge2glm.py --mqtt_broker 192.168.0.100 --mqtt_user mqtt_bridge --mqtt_pass your_password
```

Replace `192.168.0.100` with your Home Assistant IP address.

### Additional MQTT flags

| Flag | Default | Description |
|------|---------|-------------|
| `--mqtt_broker` | *(empty)* | Broker hostname/IP (empty = MQTT disabled) |
| `--mqtt_port` | `1883` | Broker port |
| `--mqtt_user` | *(empty)* | Username |
| `--mqtt_pass` | *(empty)* | Password |
| `--mqtt_topic` | `glm` | Topic prefix |
| `--mqtt_ha_discovery` | `true` | Auto-create HA entities |
| `--no_mqtt_ha_discovery` | | Disable auto-discovery |

## Step 5: Verify Entities in Home Assistant

Once vol20toglm connects to the broker, four entities appear automatically under a **Genelec GLM** device:

| Entity | Type | Controls |
|--------|------|----------|
| GLM Power | Switch | Power on/off |
| GLM Volume | Number | -127 to 0 dB slider |
| GLM Mute | Switch | Mute on/off |
| GLM Dim | Switch | Dim on/off |

To verify:
1. Go to **Settings → Devices & Services → MQTT**
2. Click on **Genelec GLM** under devices
3. All four entities should be listed with current state

You can add these to dashboards, use them in automations, or control them via voice assistants connected to HA.

## Troubleshooting

| Issue | Cause | Solution |
|-------|-------|----------|
| No entities appear | vol20toglm not connected | Check broker IP, username, password in vol20toglm logs |
| Entities appear but show "unavailable" | vol20toglm stopped | Restart vol20toglm — it publishes availability on connect |
| Connection refused | Wrong port or firewall | Verify port 1883 is open, broker is running |
| Authentication failed | Wrong credentials | Verify username/password match the HA user created in Step 2 |
| Entities appear then disappear | MQTT discovery disabled | Ensure `--no_mqtt_ha_discovery` is NOT set |
