# Fronius client

This client is for retrieving the solar energy measurements from Fronius Galvo.
Either by scraping the www.solarweb.com (assuming you have registered your inverter),
or by accepting SolarApi v1 Pushes directly from the inverter.

## Install

    go get -u github.com/tgulacsi/fronius

## Accepting pushes
[Settings](./screencapture-192-168-1-99-admincgi-bin-1450003121424.png) for the inverter,
and start the receiver: `INFLUX_USER=influxusername INFLUX_PASSW=influxuserpassword fronius serve`.

This will accept the pushes from the inverter, and store the data in the specified InfluxDB.
