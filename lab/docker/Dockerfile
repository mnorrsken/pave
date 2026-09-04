# A machine for the lab: sshd so ansible reaches it the way it reaches a real
# host, and python3 because that is what the modules run in. Nothing else —
# every playbook in lab/workspace sticks to ansible.builtin.
FROM debian:12-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates openssh-server python3 python3-apt sudo \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /run/sshd \
 && useradd --create-home --shell /bin/bash ansible \
 && printf 'ansible ALL=(ALL) NOPASSWD:ALL\n' > /etc/sudoers.d/ansible \
 && chmod 0440 /etc/sudoers.d/ansible

# The key make lab-up generated on the host. It is the only way in: password
# login stays off, so nothing here is reachable without that file.
COPY .ssh/id_ed25519.pub /home/ansible/.ssh/authorized_keys
RUN chown -R ansible:ansible /home/ansible/.ssh \
 && chmod 700 /home/ansible/.ssh \
 && chmod 600 /home/ansible/.ssh/authorized_keys

EXPOSE 22

# Fresh host keys on every start; the lab's ansible.cfg does not check them.
CMD ["sh", "-c", "ssh-keygen -A >/dev/null && exec /usr/sbin/sshd -D -e"]
