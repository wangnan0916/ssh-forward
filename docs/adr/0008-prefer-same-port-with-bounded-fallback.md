# Prefer the remote port with bounded local fallback

A Forward will first attempt to bind its Remote Listener's port on the Local Machine, then atomically try the next 100 ports in ascending order when that port is occupied. A policy may require the exact same port, and an allocated fallback remains stable for the current Listener Lifetime rather than migrating when the preferred port becomes free. This preserves predictable localhost URLs when possible while avoiding unnecessary failures in the common conflict case.
