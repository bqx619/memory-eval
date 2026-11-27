#!/bin/bash
# Start a long-running command fully detached from the calling shell: new session (setsid), double fork so the
# process is re-parented to launchd/init, stdin from /dev/null, stdout+stderr to LOGFILE (truncated), its pid in PIDFILE.
# Needed because a `nohup … &` job started from an agent harness (Codex) dies with the harness's process group when
# that agent's turn ends (observed 2026-10-26 09:45: the P16 evaluator and its script were killed 11 min in). macOS has
# no setsid(1), so perl's POSIX::setsid is used. Usage: scripts/detach.sh LOGFILE PIDFILE CMD [ARGS…]
set -u
[ $# -ge 3 ] || { echo "usage: $0 LOGFILE PIDFILE CMD [ARGS...]" >&2; exit 2; }
LOGFILE=$1 PIDFILE=$2; shift 2
exec perl -e '
  use strict; use POSIX qw(setsid _exit);
  use File::Basename qw(dirname); use File::Path qw(make_path);
  my ($log, $pidfile, @cmd) = @ARGV;
  for my $path ($log, $pidfile) {
    my $dir = dirname($path);
    eval { make_path($dir); 1 } or die "mkdir $dir: $@";
    -d $dir or die "mkdir $dir: directory not created\n";
  }
  my $pid = fork(); defined $pid or die "fork: $!"; if ($pid) { waitpid($pid, 0); _exit(0) }   # parent returns at once
  setsid() or die "setsid: $!";
  $pid = fork(); defined $pid or die "fork: $!"; _exit(0) if $pid;                           # second fork: no session leader
  open my $launcher_out, ">&", \*STDOUT or die "duplicate launcher stdout: $!";
  chdir "." ; open STDIN, "<", "/dev/null" or die; open STDOUT, ">", $log or die "open $log: $!"; open STDERR, ">&", \*STDOUT or die;
  open my $pf, ">", $pidfile or die "open $pidfile: $!"; print $pf "$$\n"; close $pf;
  print $launcher_out "$$\n"; close $launcher_out;
  exec @cmd or die "exec: $!";
' -- "$LOGFILE" "$PIDFILE" "$@"
