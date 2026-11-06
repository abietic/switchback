def daemon_listeners(pid, allowed):
    owned = set()
    for fd in Path('/proc/' + str(pid) + '/fd').iterdir():
        try:
            link = os.readlink(fd)
        except FileNotFoundError:
            continue
        if link.startswith('socket:['):
            owned.add(link[8:-1])
    unix = []
    for line in Path('/proc/net/unix').read_text().splitlines()[1:]:
        c = line.split()
        if len(c) >= 7 and c[6] in owned and int(c[3], 16) & 0x10000:
            require(len(c) == 8 and c[7] in allowed, 'extra daemon Unix listener')
            unix.append(c[7])
    require(set(unix) == set(allowed), 'missing/extra owned Unix listeners')
    for table in ['tcp', 'tcp6', 'udp', 'udp6']:
        for line in (Path('/proc/net') / table).read_text().splitlines()[1:]:
            c = line.split()
            require(len(c) > 9 and c[9] not in owned, 'daemon network socket outside private Unix profile')
    sockets = []
    for name in allowed:
        s = Path(name).lstat()
        require(stat.S_ISSOCK(s.st_mode) and s.st_uid == 0 and s.st_gid == 0
                and stat.S_IMODE(s.st_mode) in {0o600, 0o660, 0o700}, 'actual private socket type/owner/mode')
        safe_root_dir(Path(name).parent, 0o700)
        sockets.append({'path': name, 'uid': s.st_uid, 'gid': s.st_gid, 'mode': oct(stat.S_IMODE(s.st_mode)), 'type': 'unix_socket'})
    return sockets
