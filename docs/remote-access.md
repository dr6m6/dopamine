# Work from a laptop

`dopamine-remote` attaches to dopamine running on the home computer over ordinary OpenSSH. Projects, uncommitted files, Codex credentials, VPN integration, shells and dev servers stay on that computer. The laptop needs Python 3 and OpenSSH; opening dev sites also needs Firefox. Void Linux does not need Nix or a local dopamine/Codex installation to use this client.

The home computer must remain powered on and reachable. Wake-on-LAN, power profiles and automatic sleep are outside this feature. A disconnected laptop leaves home tmux workspaces running. Re-run `dopamine-remote` to reconnect; this is not automatic reconnection or persistence across a home reboot. Simultaneous connections share the same home tmux session, active tab and layout.

## Prepare the connection

Use the existing home Tailscale and OpenSSH services if present. Connect the laptop to the same Tailscale account and authorize its public SSH key for the normal home user. Ordinary SSH authentication and host-key checking remain in control; the client does not enable Tailscale SSH, change firewall rules, edit `~/.ssh/config`, copy private keys or expose a public web service.

On a fresh Void laptop, install the needed packages using its existing package and service configuration:

```sh
sudo xbps-install -S python3 openssh tailscale firefox git
```

If tailscaled is not already enabled, follow the existing runit setup. For the normal system runsvdir, enabling it is:

```sh
sudo ln -s /etc/sv/tailscaled /var/service/tailscaled
sudo sv up tailscaled
sudo tailscale up --accept-dns=false
```

Do not replace an existing service link or reset an already configured Tailscale connection. Complete the browser sign-in with the same account as the home PC. Check `tailscale status`. Keep DNS under the laptop's existing network/VPN manager: `--accept-dns=false` prevents Tailscale from replacing its resolver with MagicDNS. Use the home Tailscale IP for the connection; automatic resolution of MagicDNS names is not assumed. This does not require router changes or manual public port forwarding. Home userspace Tailscale can keep its existing configuration; the laptop normally uses the standard TUN setup.

Create a dedicated laptop SSH key if one is not already available, preserving existing keys:

```sh
ssh-keygen -t ed25519 -f ~/.ssh/dopamine-home -C dopamine-remote
```

Choose its passphrase interactively. Give only `dopamine-home.pub` to the home side. Append that public key to the home user's existing authorized keys without replacing other keys. Never send the private key. Follow the system's existing declarative authorized-key convention where one is configured.

Before accepting a new home host key, compare the fingerprint shown by SSH with the fingerprint read on the home PC:

```sh
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

## Install the laptop client

From this checkout:

```sh
./install-remote.sh
export PATH="$HOME/.local/bin:$PATH"
dopamine-remote setup USER@HOME_TAILSCALE_IP --identity ~/.ssh/dopamine-home
dopamine-remote check
dopamine-remote
```

Replace `USER@HOME_TAILSCALE_IP` with the normal home account and the address from home `tailscale ip -4`. An existing SSH alias also works, including its configured port, identity, proxy and host-key rules. Use `--port`, `--identity`, `--ssh-config` or `--command ~/path/to/dopamine` only when an explicit override is needed.

`check` executes the home command and reads its project registry. The client finds `~/.local/bin/dopamine` first and otherwise uses the home shell's PATH. A source installation may perform its usual pinned Nix build on the home computer. Keep the source clone there.

Settings are saved privately in `~/.config/dopamine/remote.json`, honoring `XDG_CONFIG_HOME`; `--config PATH` selects another file. They contain a destination and paths, not key material. Re-running setup updates supplied fields and preserves other fields. Edit the file to remove an old optional override. The client is copied into `~/.local/bin` and can run independently of this checkout. Re-run the installer to update it; modified or unrelated installed commands are protected from replacement. `dopamine-remote install --uninstall` removes the owned client and preserves settings and all home data.

## Open a home dev site in laptop Firefox

Start the server from the remote dopamine project menu. In a second laptop terminal:

```sh
dopamine-remote projects
dopamine-remote open PROJECT_ID
```

This reads that project's `app_url`, creates an SSH local forward bound only to laptop `127.0.0.1`, and opens the corresponding local URL in Firefox. The path, query and fragment are preserved. Keep this terminal open while using the site. Ctrl+C stops only this tunnel, retaining the home dev server and workspaces.

The default laptop port matches the home server port. If it is occupied, choose another explicitly:

```sh
dopamine-remote open PROJECT_ID --local-port 15173
```

Use `--no-browser` to print the URL and keep the tunnel without launching a browser. No other browser is selected automatically. Loopback HTTP(S) targets and dopamine's existing `10.203.31.1:8420` VPN relay are supported. The forwarded target must be reachable from the home SSH server; a service confined inside a separate VPN namespace still needs its existing host relay. A different laptop port or hostname can require the project's usual dev-server origin/HMR configuration. HTTPS still uses the project's own certificate, which must be valid for the local URL.

The TUI's existing **open url** action still invokes the home desktop handler. Use `dopamine-remote open` for the laptop browser. The client neither starts a stopped server nor changes its configuration.

## Diagnose connection problems

- `tailscale status`: confirm both devices are connected to the expected account and permitted by tailnet policy.
- `ssh USER@HOME_TAILSCALE_IP`: verify the network, public key and host fingerprint independently of dopamine. A host key change must be investigated, not bypassed.
- `dopamine-remote check`: distinguish a working SSH connection from a missing home launcher or invalid registry.
- `dopamine-remote projects`: use the exact project ID; only projects with an `app_url` can be opened.
- An occupied local port fails explicitly. Choose `--local-port`; the client does not silently attach Firefox to an unrelated listener.
- Lost SSH connections are detected with keepalives. Re-run the client or `open` command after the network returns.

If DNS stops working after a reboot or VPN connection, inspect `/etc/resolv.conf` and check `getent ahostsv4 chatgpt.com`. A resolver containing only `100.100.100.100` and `fd7a:115c:a1e0::53` is using Tailscale's DNS. If that resolver is unreachable, it can also prevent local Codex from starting (`workspace routing discovery failed`) and stop Tailscale from reaching its control plane. For this IP-based remote setup, return DNS management to the existing network/VPN setup without disconnecting Tailscale:

```sh
sudo tailscale set --accept-dns=false
sudo resolvconf -u
cat /etc/resolv.conf
getent ahostsv4 chatgpt.com
```

The `resolvconf -u` command applies to this Void/openresolv setup. Inspect the generated resolver and test the actual lookup; a successful VPN handshake alone does not establish working DNS or provider connectivity. Do not replace `/etc/resolv.conf` manually or change the VPN profile as a first step. See [Tailscale's resolver guidance](https://tailscale.com/docs/reference/faq/dns-resolv-conf).

If Tailscale ping works but SSH times out while AmneziaWG is active, inspect `ip route get HOME_TAILSCALE_IP`, `ip -4 rule show` and `ip -4 route show table 52`. On the owner's laptop, automatically allocated AWG rules at priorities 5208/5209 preceded Tailscale's table-52 rule at 5270, sending the home address through the VPN interface. A daemon-mediated `tailscale nc` probe also timed out in that configuration.

The owner's existing Void `vpn run` wrapper now restores a specific home IPv4 `/32` route through `tailscale0` in the main table, but only when the same home route already exists in Tailscale's table 52. AWG's existing `main suppress_prefixlength 0` rule then selects this host route before its VPN table. The wrapper reads the numeric home address from `remote.json`, preserves a conflicting existing main route, and treats this integration as optional when reporting VPN status. It does not rewrite VPN policy rules or profiles. Run Tailscale before `vpn run`; if it connects later or recreates its interface, repeat `vpn run`. This is restoration by the VPN command rather than an independent boot-time service.

## Implementation and verification

Reuse OpenSSH for encryption, authentication, PTYs, port forwarding and connection liveness; reuse the existing home tmux lifecycle. A standalone Python standard-library client keeps the Void installation small and avoids another server, synchronization service or terminal implementation. A fresh Nix installation can also expose the client from the package. No Python dependency is downloaded.

Sources checked for this choice: [OpenSSH](https://man.openbsd.org/ssh), [SSH configuration](https://man.openbsd.org/ssh_config), [Tailscale connectivity](https://tailscale.com/docs/how-to/connect-to-devices), [Void runit services](https://docs.voidlinux.org/config/services/index.html). Sources are used as implementation references; no third-party implementation is vendored.

`make test` includes a real isolated SSH server with disposable host/client keys, actual dopamine/tmux keyboard input, server launch, an interrupted connection, reconnection to the same shell PID, real HTTP through a local SSH forward, a Firefox invocation fixture, and tunnel cleanup. These checks do not log into a real tailnet, test a physical Void laptop, or launch a personal browser profile. The home/laptop installation still needs the actual account login, key enrollment and external-network check described above.

A separate check on the owner's NixOS home PC and actual Void Linux laptop passed over Tailscale on the same home LAN. The laptop authenticated with its dedicated key and verified the home host fingerprint, ran `check`, and opened the real home dashboard. An isolated home workspace retained the same shell PID and a shell variable across an interrupted SSH connection and a fresh connection. A dev server started through the TUI returned HTTP 200 through the laptop tunnel, and its page was visually checked in Firefox with a temporary profile. Closing the tunnel released the local port while leaving the home server available; the disposable processes were then stopped. The home side also checked the SSH authentication log, matching shell evidence and successful HTTP requests independently of the laptop report.

The laptop was subsequently tested on a mobile hotspot. Tailscale answered through DERP Frankfurt at 226–250 ms; direct connectivity was not established during that probe. After correcting the AWG/Tailscale route conflict, `check` read the real home registry and the owner confirmed that the home TUI opened and accepted keyboard input. Successful SSH authentications were independently observed on the home PC. The updated VPN wrapper was then installed, its specific main-table route was verified, the temporary diagnostic policy rule was removed, and `check` passed again with AmneziaWG active. A reboot after this routing change was not performed. Forwarded dev-site rendering was checked on the home LAN; it was not repeated on the mobile hotspot. Sleep, wake-up and home reboot recovery remain outside this feature.

A reboot-time DNS failure was also reproduced on the Void laptop: its resolver contained only Tailscale DNS addresses, local Codex reported a DNS lookup failure, and the laptop appeared offline in the tailnet despite a running tailscaled service and a recent VPN handshake. Applying `tailscale set --accept-dns=false` and regenerating the openresolv resolver restored the existing VPN DNS servers. The laptop then resolved `chatgpt.com`, connected its Codex Responses WebSocket with HTTP 101, and passed all four network-only doctor checks. The owner then confirmed that the usual `codex --not-so-yolo` command opened the interface without the routing error.
