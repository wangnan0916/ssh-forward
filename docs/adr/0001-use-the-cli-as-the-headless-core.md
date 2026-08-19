# Use the CLI as the headless product core

The new product's Go CLI will be its headless core. It will own remote discovery, saved forwarding policies, reconciliation, and SSH control behind a versioned JSON interface; the WebUI and desktop app will own presentation and interaction only. This preserves one behavioral source of truth and avoids duplicating SSH logic without depending on the pre-existing shell utility.
