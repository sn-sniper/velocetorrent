package ipc

import (
	"fmt"
	"net/http"
	"strings"
)

// HandlePrometheusMetrics serves lock-free atomic engine counters in Prometheus 0.0.4 text format on GET /metrics.
func (s *Server) HandlePrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snap := s.eng.Snapshot(true)

	var tcpPeers, utpPeers int
	for _, t := range snap.Torrents {
		for _, p := range t.Peers {
			if strings.Contains(strings.ToUpper(p.Transport), "UTP") {
				utpPeers++
			} else {
				tcpPeers++
			}
		}
	}

	var b strings.Builder
	b.WriteString("# HELP velocetorrent_uptime_seconds Seconds since VeloceTorrent daemon startup.\n")
	b.WriteString("# TYPE velocetorrent_uptime_seconds counter\n")
	fmt.Fprintf(&b, "velocetorrent_uptime_seconds %d\n\n", snap.UptimeSeconds)

	b.WriteString("# HELP velocetorrent_global_download_bytes_per_second Current global download velocity in bytes/sec.\n")
	b.WriteString("# TYPE velocetorrent_global_download_bytes_per_second gauge\n")
	fmt.Fprintf(&b, "velocetorrent_global_download_bytes_per_second %d\n\n", snap.GlobalDownBytesPs)

	b.WriteString("# HELP velocetorrent_global_upload_bytes_per_second Current global upload velocity in bytes/sec.\n")
	b.WriteString("# TYPE velocetorrent_global_upload_bytes_per_second gauge\n")
	fmt.Fprintf(&b, "velocetorrent_global_upload_bytes_per_second %d\n\n", snap.GlobalUpBytesPs)

	b.WriteString("# HELP velocetorrent_total_downloaded_bytes Cumulative bytes downloaded across all sessions.\n")
	b.WriteString("# TYPE velocetorrent_total_downloaded_bytes counter\n")
	fmt.Fprintf(&b, "velocetorrent_total_downloaded_bytes %d\n\n", snap.TotalDownloaded)

	b.WriteString("# HELP velocetorrent_total_uploaded_bytes Cumulative bytes uploaded across all sessions.\n")
	b.WriteString("# TYPE velocetorrent_total_uploaded_bytes counter\n")
	fmt.Fprintf(&b, "velocetorrent_total_uploaded_bytes %d\n\n", snap.TotalUploaded)

	b.WriteString("# HELP velocetorrent_active_peers Total connected swarm peers.\n")
	b.WriteString("# TYPE velocetorrent_active_peers gauge\n")
	fmt.Fprintf(&b, "velocetorrent_active_peers %d\n\n", snap.GlobalActivePeers)

	b.WriteString("# HELP velocetorrent_swarm_peers Total known peers across all active swarms.\n")
	b.WriteString("# TYPE velocetorrent_swarm_peers gauge\n")
	fmt.Fprintf(&b, "velocetorrent_swarm_peers %d\n\n", snap.GlobalSwarmPeers)

	b.WriteString("# HELP velocetorrent_peer_transport_connections Active peer connections partitioned by transport stack.\n")
	b.WriteString("# TYPE velocetorrent_peer_transport_connections gauge\n")
	fmt.Fprintf(&b, "velocetorrent_peer_transport_connections{transport=\"TCP+BBR\"} %d\n", tcpPeers)
	fmt.Fprintf(&b, "velocetorrent_peer_transport_connections{transport=\"uTP\"} %d\n\n", utpPeers)

	b.WriteString("# HELP velocetorrent_disk_write_queue_depth Pending pieces in sequential NVMe disk flush queue.\n")
	b.WriteString("# TYPE velocetorrent_disk_write_queue_depth gauge\n")
	fmt.Fprintf(&b, "velocetorrent_disk_write_queue_depth %d\n\n", snap.GlobalDiskQueue)

	b.WriteString("# HELP velocetorrent_hash_verify_queue_depth Pending pieces in SHA-1/SHA-256 verification worker pool.\n")
	b.WriteString("# TYPE velocetorrent_hash_verify_queue_depth gauge\n")
	fmt.Fprintf(&b, "velocetorrent_hash_verify_queue_depth %d\n\n", snap.GlobalHashQueue)

	b.WriteString("# HELP velocetorrent_disk_iops Disk read/write operations per second.\n")
	b.WriteString("# TYPE velocetorrent_disk_iops gauge\n")
	fmt.Fprintf(&b, "velocetorrent_disk_iops %d\n\n", snap.GlobalIOPS)

	b.WriteString("# HELP velocetorrent_piece_verify_latency_microseconds_avg EWMA piece SHA verification latency in microseconds.\n")
	b.WriteString("# TYPE velocetorrent_piece_verify_latency_microseconds_avg gauge\n")
	fmt.Fprintf(&b, "velocetorrent_piece_verify_latency_microseconds_avg %d\n\n", snap.AvgVerifyLatencyUs)

	b.WriteString("# HELP velocetorrent_blocklist_prefixes Loaded CIDR prefixes in the IP blocklist radix tree.\n")
	b.WriteString("# TYPE velocetorrent_blocklist_prefixes gauge\n")
	fmt.Fprintf(&b, "velocetorrent_blocklist_prefixes %d\n\n", snap.BlocklistRanges)

	b.WriteString("# HELP velocetorrent_blocklist_dropped_peers_total Total peer connection attempts dropped by radix tree filter.\n")
	b.WriteString("# TYPE velocetorrent_blocklist_dropped_peers_total counter\n")
	fmt.Fprintf(&b, "velocetorrent_blocklist_dropped_peers_total %d\n\n", snap.BlockedPeerHits)

	if len(snap.Torrents) > 0 {
		b.WriteString("# HELP velocetorrent_torrent_progress_ratio Completion percentage (0..100) per torrent.\n")
		b.WriteString("# TYPE velocetorrent_torrent_progress_ratio gauge\n")
		for _, t := range snap.Torrents {
			safeName := strings.ReplaceAll(strings.ReplaceAll(t.Name, `\`, `\\`), `"`, `\"`)
			fmt.Fprintf(&b, "velocetorrent_torrent_progress_ratio{info_hash=%q,name=\"%s\",status=%q} %.2f\n",
				t.InfoHash, safeName, t.Status, t.Progress)
		}
		b.WriteString("\n")
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}
