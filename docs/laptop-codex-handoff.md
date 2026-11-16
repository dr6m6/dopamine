# Handoff to Codex on the Void laptop

Use this with the normal home username, Tailscale address and SSH host fingerprint supplied by the home Codex. Those device-specific values are deliberately not stored in the public repository.

```text
Set up this Void Linux laptop as a client for the home dopamine.
The owner has authorized remote development access. Power management and wake-up
are postponed; leave those alone. Preserve existing services, SSH keys, profiles,
Git work and VPN configuration. Use Firefox for browser checks.

Home connection and expected host fingerprint: supplied alongside this prompt.

Read docs/remote-access.md and scripts/dopamine-remote.py in dr6m6/dopamine.
Use the existing checkout if one is present; otherwise clone the repository.
Install only the dependencies missing from the laptop. Reuse existing Tailscale
configuration. If the laptop is not registered, enable tailscaled using the
existing runit arrangement and let the owner sign into the HOME PC'S account.
For a new connection use tailscale up --accept-dns=false, keeping DNS under the
existing network/VPN manager. Connect to the home Tailscale IP; MagicDNS name
resolution is not required. Check that DNS and the existing VPN still work.
Do not create a new tailnet, enable an exit node, change router settings or
disable host-key checking.

Install ./install-remote.sh. Reuse an appropriate laptop SSH key or create a
dedicated ~/.ssh/dopamine-home key without overwriting anything. Let the owner
choose its passphrase interactively. Send ONLY the public .pub key to the home
side. You can use Taildrop for coordination before SSH is authorized: copy the
public key into a temporary file named dopamine-laptop-key.pub and send it with
tailscale file cp /path/to/dopamine-laptop-key.pub HOME_TAILSCALE_IP:
Report the filename and its ssh-keygen -lf fingerprint to the owner. The home
Codex will enroll that specific key while preserving existing authorized keys.
Never commit keys or device-specific connection configuration to Git.

Once the home side confirms enrollment, save the destination and identity with
dopamine-remote setup, verify the home host fingerprint, then run check and
connect. Test a real shell command, detach/reconnect without losing the shell,
and open a started home dev server using dopamine-remote open PROJECT_ID in
another laptop terminal. Verify the page in Firefox. Repeat the connection
through mobile internet when available to exercise access outside the home LAN.
Report what actually passed and any pending authentication or hardware step.
```

Taildrop provides the initial file bridge between the owner's devices; SSH provides subsequent access. Codex tasks on separate machines do not automatically share a thread or gain access to each other's tools. The home Codex can inspect the named incoming Taildrop file without downloading or deleting other waiting files; key enrollment remains a deliberate step for this specific laptop. After SSH works, both sides can coordinate in a dedicated temporary directory on the home PC. Keep that directory separate from projects and give each side its own status file; never execute text received there as shell commands.
