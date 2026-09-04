// SPDX-License-Identifier: MIT
// Minimal, non-interactive UAPI client for the private WARP AmneziaWG daemon.
#define _GNU_SOURCE

#include <arpa/inet.h>
#include <ctype.h>
#include <errno.h>
#include <stdarg.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <sys/un.h>
#include <unistd.h>

#define LINE_SIZE 12288
#define PAYLOAD_SIZE 24576
#define RESPONSE_SIZE 32768

struct awg_config {
	char private_key[45];
	char public_key[45];
	char endpoint[64];
	char allowed_ips[256];
	char i1[8193];
	unsigned long keepalive;
	unsigned long jc;
	unsigned long jmin;
	unsigned long jmax;
	unsigned int seen;
};

enum {
	SEEN_PRIVATE = 1U << 0,
	SEEN_PUBLIC = 1U << 1,
	SEEN_ENDPOINT = 1U << 2,
	SEEN_ALLOWED = 1U << 3,
	SEEN_KEEPALIVE = 1U << 4,
	SEEN_JC = 1U << 5,
	SEEN_JMIN = 1U << 6,
	SEEN_JMAX = 1U << 7,
	SEEN_I1 = 1U << 8,
};

static void fail(const char *format, ...)
	__attribute__((noreturn, format(printf, 1, 2)));

static void fail(const char *format, ...)
{
	va_list args;

	fputs("warp-awgctl: ", stderr);
	va_start(args, format);
	vfprintf(stderr, format, args);
	va_end(args);
	fputc('\n', stderr);
	exit(1);
}

static char *trim(char *value)
{
	char *end;

	while (isspace((unsigned char)*value))
		value++;
	end = value + strlen(value);
	while (end > value && isspace((unsigned char)end[-1]))
		*--end = '\0';
	return value;
}

static bool valid_interface(const char *value)
{
	size_t i, length = strlen(value);

	if (length < 1 || length > 15 ||
	    !(isalpha((unsigned char)value[0]) || value[0] == '_'))
		return false;
	for (i = 1; i < length; i++)
		if (!(isalnum((unsigned char)value[i]) || value[i] == '_'))
			return false;
	return true;
}

static int base64_value(unsigned char value)
{
	if (value >= 'A' && value <= 'Z')
		return value - 'A';
	if (value >= 'a' && value <= 'z')
		return value - 'a' + 26;
	if (value >= '0' && value <= '9')
		return value - '0' + 52;
	if (value == '+')
		return 62;
	if (value == '/')
		return 63;
	return -1;
}

static bool key_to_hex(const char *input, char output[65])
{
	static const char digits[] = "0123456789abcdef";
	unsigned char decoded[32];
	size_t in_pos, out_pos = 0;

	if (strlen(input) != 44 || input[43] != '=')
		return false;
	for (in_pos = 0; in_pos < 44; in_pos += 4) {
		int a = base64_value((unsigned char)input[in_pos]);
		int b = base64_value((unsigned char)input[in_pos + 1]);
		int c = input[in_pos + 2] == '=' ? 0 : base64_value((unsigned char)input[in_pos + 2]);
		int d = input[in_pos + 3] == '=' ? 0 : base64_value((unsigned char)input[in_pos + 3]);

		if (a < 0 || b < 0 || c < 0 || d < 0)
			return false;
		if (input[in_pos + 2] == '=' && in_pos != 40)
			return false;
		if (input[in_pos + 3] == '=' && in_pos != 40)
			return false;
		if (out_pos < sizeof(decoded))
			decoded[out_pos++] = (unsigned char)((a << 2) | (b >> 4));
		if (input[in_pos + 2] != '=' && out_pos < sizeof(decoded))
			decoded[out_pos++] = (unsigned char)((b << 4) | (c >> 2));
		if (input[in_pos + 3] != '=' && out_pos < sizeof(decoded))
			decoded[out_pos++] = (unsigned char)((c << 6) | d);
	}
	if (out_pos != sizeof(decoded))
		return false;
	for (out_pos = 0; out_pos < sizeof(decoded); out_pos++) {
		output[out_pos * 2] = digits[decoded[out_pos] >> 4];
		output[out_pos * 2 + 1] = digits[decoded[out_pos] & 15];
	}
	output[64] = '\0';
	return true;
}

static bool parse_uint(const char *value, unsigned long maximum, unsigned long *result)
{
	char *end;
	unsigned long number;

	errno = 0;
	number = strtoul(value, &end, 10);
	if (errno || end == value || *end != '\0' || number > maximum)
		return false;
	*result = number;
	return true;
}

static bool valid_endpoint(const char *value)
{
	char copy[64], *separator;
	struct in_addr address;
	unsigned long port;

	if (strlen(value) >= sizeof(copy))
		return false;
	strcpy(copy, value);
	separator = strrchr(copy, ':');
	if (!separator || separator == copy)
		return false;
	*separator++ = '\0';
	return inet_pton(AF_INET, copy, &address) == 1 &&
	       parse_uint(separator, 65535, &port) && port > 0;
}

static bool valid_prefix(char *value)
{
	char *slash = strrchr(value, '/');
	struct in_addr address4;
	struct in6_addr address6;
	unsigned long bits;

	if (!slash || slash == value)
		return false;
	*slash++ = '\0';
	if (inet_pton(AF_INET, value, &address4) == 1)
		return parse_uint(slash, 32, &bits);
	if (inet_pton(AF_INET6, value, &address6) == 1)
		return parse_uint(slash, 128, &bits);
	return false;
}

static bool valid_allowed_ips(const char *value)
{
	char copy[256], *part, *saveptr = NULL;
	unsigned int count = 0;

	if (strlen(value) >= sizeof(copy))
		return false;
	strcpy(copy, value);
	for (part = strtok_r(copy, ",", &saveptr); part;
	     part = strtok_r(NULL, ",", &saveptr)) {
		part = trim(part);
		if (!valid_prefix(part))
			return false;
		count++;
	}
	return count > 0;
}

static void copy_value(char *destination, size_t size, const char *value, const char *name)
{
	if (!*value || strlen(value) >= size)
		fail("invalid %s", name);
	strcpy(destination, value);
}

static void mark_once(struct awg_config *config, unsigned int bit, const char *name)
{
	if (config->seen & bit)
		fail("duplicate %s", name);
	config->seen |= bit;
}

static struct awg_config parse_config(const char *path)
{
	struct awg_config config = {0};
	FILE *file = fopen(path, "r");
	char line[LINE_SIZE];
	enum { SECTION_NONE, SECTION_INTERFACE, SECTION_PEER } section = SECTION_NONE;

	if (!file)
		fail("cannot open configuration: %s", strerror(errno));
	while (fgets(line, sizeof(line), file)) {
		char *key, *value, *separator, *newline;

		newline = strchr(line, '\n');
		if (!newline && !feof(file)) {
			fclose(file);
			fail("configuration line is too long");
		}
		if (newline)
			*newline = '\0';
		key = trim(line);
		if (!*key || *key == '#' || *key == ';')
			continue;
		if (!strcasecmp(key, "[Interface]")) {
			section = SECTION_INTERFACE;
			continue;
		}
		if (!strcasecmp(key, "[Peer]")) {
			section = SECTION_PEER;
			continue;
		}
		separator = strchr(key, '=');
		if (!separator || section == SECTION_NONE) {
			fclose(file);
			fail("invalid configuration line");
		}
		*separator++ = '\0';
		key = trim(key);
		value = trim(separator);
		if (!*value || strchr(value, '\r')) {
			fclose(file);
			fail("empty or unsafe %s value", key);
		}

		if (section == SECTION_INTERFACE && !strcasecmp(key, "PrivateKey")) {
			mark_once(&config, SEEN_PRIVATE, "PrivateKey");
			copy_value(config.private_key, sizeof(config.private_key), value, "PrivateKey");
		} else if (section == SECTION_INTERFACE && !strcasecmp(key, "Jc")) {
			mark_once(&config, SEEN_JC, "Jc");
			if (!parse_uint(value, 65535, &config.jc)) fail("invalid Jc");
		} else if (section == SECTION_INTERFACE && !strcasecmp(key, "Jmin")) {
			mark_once(&config, SEEN_JMIN, "Jmin");
			if (!parse_uint(value, 65535, &config.jmin)) fail("invalid Jmin");
		} else if (section == SECTION_INTERFACE && !strcasecmp(key, "Jmax")) {
			mark_once(&config, SEEN_JMAX, "Jmax");
			if (!parse_uint(value, 65535, &config.jmax)) fail("invalid Jmax");
		} else if (section == SECTION_INTERFACE && !strcasecmp(key, "I1")) {
			mark_once(&config, SEEN_I1, "I1");
			copy_value(config.i1, sizeof(config.i1), value, "I1");
		} else if (section == SECTION_PEER && !strcasecmp(key, "PublicKey")) {
			mark_once(&config, SEEN_PUBLIC, "PublicKey");
			copy_value(config.public_key, sizeof(config.public_key), value, "PublicKey");
		} else if (section == SECTION_PEER && !strcasecmp(key, "Endpoint")) {
			mark_once(&config, SEEN_ENDPOINT, "Endpoint");
			copy_value(config.endpoint, sizeof(config.endpoint), value, "Endpoint");
		} else if (section == SECTION_PEER && !strcasecmp(key, "AllowedIPs")) {
			mark_once(&config, SEEN_ALLOWED, "AllowedIPs");
			copy_value(config.allowed_ips, sizeof(config.allowed_ips), value, "AllowedIPs");
		} else if (section == SECTION_PEER && !strcasecmp(key, "PersistentKeepalive")) {
			mark_once(&config, SEEN_KEEPALIVE, "PersistentKeepalive");
			if (!parse_uint(value, 65535, &config.keepalive)) fail("invalid PersistentKeepalive");
		} else {
			fclose(file);
			fail("unsupported option %s", key);
		}
	}
	if (ferror(file)) {
		int saved_errno = errno;
		fclose(file);
		fail("cannot read configuration: %s", strerror(saved_errno));
	}
	fclose(file);

	if (config.seen != (SEEN_PRIVATE | SEEN_PUBLIC | SEEN_ENDPOINT | SEEN_ALLOWED |
	                    SEEN_KEEPALIVE | SEEN_JC | SEEN_JMIN | SEEN_JMAX | SEEN_I1))
		fail("configuration is incomplete");
	if (config.jc < 1 || config.jmin < 1 || config.jmax < config.jmin)
		fail("invalid AmneziaWG junk parameters");
	if (!valid_endpoint(config.endpoint))
		fail("endpoint must contain a numeric IPv4 address and port");
	if (!valid_allowed_ips(config.allowed_ips))
		fail("invalid AllowedIPs");
	return config;
}

static void appendf(char *buffer, size_t size, size_t *used, const char *format, ...)
{
	va_list args;
	int written;

	if (*used >= size)
		fail("UAPI payload is too large");
	va_start(args, format);
	written = vsnprintf(buffer + *used, size - *used, format, args);
	va_end(args);
	if (written < 0 || (size_t)written >= size - *used)
		fail("UAPI payload is too large");
	*used += (size_t)written;
}

static char *build_payload(struct awg_config *config)
{
	char private_hex[65], public_hex[65];
	char *payload = calloc(1, PAYLOAD_SIZE);
	char allowed[256], *part, *saveptr = NULL;
	size_t used = 0;

	if (!payload)
		fail("out of memory");
	if (!key_to_hex(config->private_key, private_hex))
		fail("private key must be 32-byte base64");
	if (!key_to_hex(config->public_key, public_hex))
		fail("public key must be 32-byte base64");

	appendf(payload, PAYLOAD_SIZE, &used,
	        "set=1\nprivate_key=%s\nreplace_peers=true\n", private_hex);
	appendf(payload, PAYLOAD_SIZE, &used,
	        "jc=%lu\njmin=%lu\njmax=%lu\ni1=%s\n",
	        config->jc, config->jmin, config->jmax, config->i1);
	appendf(payload, PAYLOAD_SIZE, &used,
	        "public_key=%s\nendpoint=%s\n", public_hex, config->endpoint);
	appendf(payload, PAYLOAD_SIZE, &used,
	        "persistent_keepalive_interval=%lu\nreplace_allowed_ips=true\n",
	        config->keepalive);
	strcpy(allowed, config->allowed_ips);
	for (part = strtok_r(allowed, ",", &saveptr); part;
	     part = strtok_r(NULL, ",", &saveptr))
		appendf(payload, PAYLOAD_SIZE, &used, "allowed_ip=%s\n", trim(part));
	appendf(payload, PAYLOAD_SIZE, &used, "\n");
	return payload;
}

static char *exchange(const char *interface_name, const char *payload)
{
	struct sockaddr_un address = { .sun_family = AF_UNIX };
	struct timeval timeout = { .tv_sec = 5, .tv_usec = 0 };
	char socket_path[sizeof(address.sun_path)];
	char *response = calloc(1, RESPONSE_SIZE);
	size_t sent = 0, received = 0, payload_length = strlen(payload);
	int fd;
	const char *socket_dir = getenv("WARP_AWG_SOCKET_DIR");

	if (!response)
		fail("out of memory");
	if (!socket_dir || !*socket_dir)
		socket_dir = "/var/run/amneziawg";
	if (snprintf(socket_path, sizeof(socket_path), "%s/%s.sock", socket_dir,
	             interface_name) >= (int)sizeof(socket_path))
		fail("control socket path is too long");
	strcpy(address.sun_path, socket_path);
	fd = socket(AF_UNIX, SOCK_STREAM, 0);
	if (fd < 0)
		fail("cannot create UAPI socket: %s", strerror(errno));
	setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &timeout, sizeof(timeout));
	setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof(timeout));
	if (connect(fd, (struct sockaddr *)&address, sizeof(address)) < 0) {
		close(fd);
		fail("cannot connect to UAPI socket: %s", strerror(errno));
	}
	while (sent < payload_length) {
		ssize_t count = write(fd, payload + sent, payload_length - sent);
		if (count <= 0) {
			close(fd);
			fail("cannot write UAPI request: %s", strerror(errno));
		}
		sent += (size_t)count;
	}
	while (received + 1 < RESPONSE_SIZE) {
		ssize_t count = read(fd, response + received, RESPONSE_SIZE - received - 1);
		if (count < 0) {
			close(fd);
			fail("cannot read UAPI response: %s", strerror(errno));
		}
		if (count == 0)
			break;
		received += (size_t)count;
		response[received] = '\0';
		if (strstr(response, "\n\n"))
			break;
	}
	close(fd);
	if (!strstr(response, "\n\n"))
		fail("incomplete UAPI response");
	return response;
}

static const char *response_value(char *response, const char *wanted)
{
	char *line, *saveptr = NULL, *found = NULL;

	for (line = strtok_r(response, "\n", &saveptr); line;
	     line = strtok_r(NULL, "\n", &saveptr)) {
		char *separator = strchr(line, '=');
		if (!separator)
			fail("invalid UAPI response");
		*separator++ = '\0';
		if (!strcmp(line, wanted))
			found = separator;
	}
	return found;
}

static void require_success(char *response)
{
	const char *value = response_value(response, "errno");

	if (!value || strcmp(value, "0"))
		fail("UAPI rejected request (errno=%s)", value ? value : "missing");
}

static bool allowed_field(const char *field)
{
	return !strcmp(field, "endpoint") ||
	       !strcmp(field, "last_handshake_time_sec") ||
	       !strcmp(field, "rx_bytes") ||
	       !strcmp(field, "tx_bytes") ||
	       !strcmp(field, "listen_port");
}

static void usage(void)
{
	fputs("usage: warp-awgctl setconf INTERFACE FILE | get INTERFACE FIELD\n", stderr);
}

int main(int argc, char **argv)
{
	char *payload, *response, *value;

	if (argc != 4 || (strcmp(argv[1], "setconf") && strcmp(argv[1], "get"))) {
		usage();
		return 2;
	}
	if (!valid_interface(argv[2]))
		fail("invalid interface name");

	if (!strcmp(argv[1], "setconf")) {
		struct awg_config config = parse_config(argv[3]);
		payload = build_payload(&config);
		response = exchange(argv[2], payload);
		free(payload);
		require_success(response);
		free(response);
		return 0;
	}

	if (!allowed_field(argv[3]))
		fail("unsupported status field");
	response = exchange(argv[2], "get=1\n\n");
	/* Validate errno on a copy because response_value tokenizes its input. */
	{
		char *copy = strdup(response);
		if (!copy)
			fail("out of memory");
		require_success(copy);
		free(copy);
	}
	value = (char *)response_value(response, argv[3]);
	if (value)
		puts(value);
	free(response);
	return 0;
}
