class NotifyRelay < Formula
  desc "Forward notify-send notifications to a host desktop session"
  homepage "https://github.com/xtruder/notify-relay"
  head "https://github.com/xtruder/notify-relay.git", branch: "main"
  license "Apache-2.0"

  depends_on "go" => :build

  def install
    timestamp = Time.now.utc.strftime("%Y-%m-%dT%H:%M:%SZ")
    ldflags = %W[
      -s
      -w
      -X github.com/xtruder/notify-relay/internal/buildinfo.Version=head
      -X github.com/xtruder/notify-relay/internal/buildinfo.Commit=homebrew
      -X github.com/xtruder/notify-relay/internal/buildinfo.Date=#{timestamp}
    ]

    system "go", "build", *std_go_args(ldflags: ldflags), "./cmd/notify-relay"
    pkgshare.install "packaging/systemd/notify-relayd.service"
  end

  test do
    assert_match "notify-relay version=", shell_output("#{bin}/notify-relay --version 2>&1")
  end

  def caveats
    <<~EOS
      `notify-relay` works on Linux and macOS clients.
      The `serve` subcommand talks to org.freedesktop.Notifications and is intended for Linux hosts.
      The packaged systemd unit is installed at:
        #{pkgshare}/notify-relayd.service
      If you want it to replace `notify-send`, create a symlink manually:
        ln -s #{opt_bin}/notify-relay #{HOMEBREW_PREFIX}/bin/notify-send
    EOS
  end
end
