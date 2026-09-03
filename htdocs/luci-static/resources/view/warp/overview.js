'use strict';
'require form';
'require rpc';
'require uci';
'require ui';
'require view';

const callStatus = rpc.declare({
	object: 'luci.warp',
	method: 'status'
});

const callRegister = rpc.declare({
	object: 'luci.warp',
	method: 'register',
	params: [ 'accept_terms' ]
});

const callEnable = rpc.declare({ object: 'luci.warp', method: 'enable' });
const callDisable = rpc.declare({ object: 'luci.warp', method: 'disable' });
const callReconnect = rpc.declare({ object: 'luci.warp', method: 'reconnect' });
const callUnregister = rpc.declare({ object: 'luci.warp', method: 'unregister' });

const stateLabels = {
	not_configured: _('Not configured'),
	registering: _('Registering'),
	interface_created: _('Interface created'),
	interface_up: _('Interface is up'),
	disabled: _('Disabled'),
	error: _('Error')
};

const backendLabels = {
	amneziawg: _('WARP AmneziaWG')
};

const errorLabels = {
	operation_in_progress: _('Another WARP operation is already in progress.'),
	dependency_missing: _('A required package is missing.'),
	ca_bundle_missing: _('The CA certificate bundle is missing.'),
	tun_unavailable: _('The TUN device is unavailable. Check kmod-tun.'),
	system_time_invalid: _('System time is invalid. Wait for NTP synchronization and try again.'),
	dns_error: _('Cloudflare API name resolution failed.'),
	connection_error: _('Could not connect to the Cloudflare API.'),
	timeout: _('The Cloudflare API request timed out.'),
	tls_error: _('TLS verification or negotiation with Cloudflare failed.'),
	network_error: _('A network error occurred while contacting Cloudflare.'),
	api_rate_limited: _('Cloudflare temporarily rate-limited registration requests.'),
	api_request_rejected: _('Cloudflare rejected the registration request.'),
	api_unavailable: _('The Cloudflare registration service is temporarily unavailable.'),
	unexpected_http_status: _('Cloudflare returned an unexpected HTTP status.'),
	invalid_api_response: _('Cloudflare returned an incomplete or invalid response.'),
	invalid_awg_account: _('The WARP account is missing or invalid.'),
	invalid_awg_config: _('The AmneziaWG configuration is missing or invalid.'),
	awg_registration_failed: _('WARP registration failed. Check /etc/warp/backend.log.'),
	endpoint_scan_failed: _('No suitable WARP endpoint was found. Check /etc/warp/backend.log.'),
	invalid_backend: _('The stored backend name is invalid.'),
	invalid_auto_start: _('The automatic start setting is invalid.'),
	invalid_interface_name: _('The preferred interface name is invalid.'),
	invalid_mtu: _('The configured MTU is invalid.'),
	invalid_keepalive: _('The configured keepalive value is invalid.'),
	invalid_sni: _('The masking hostname is invalid.'),
	invalid_excluded_countries: _('The excluded country list is invalid.'),
	invalid_excluded_nodes: _('The excluded endpoint list is invalid.'),
	invalid_scan_sample: _('The endpoint scan size is invalid.'),
	no_safe_interface_name: _('No safe interface name is available.'),
	managed_interface_missing: _('The managed interface is missing.'),
	backend_start_failed: _('The WARP tunnel service could not be started.'),
	tunnel_start_timeout: _('Timed out waiting for the WARP tunnel interface. Check the system log.'),
	data_plane_unavailable: _('The selected endpoint did not pass traffic.'),
	interface_up_failed: _('The interface could not be brought up.'),
	network_reload_failed: _('netifd could not reload the network configuration.'),
	network_snapshot_failed: _('The managed network sections could not be backed up.'),
	rollback_failed: _('The network configuration rollback failed.'),
	uci_commit_failed: _('The network configuration could not be committed.'),
	uci_stage_failed: _('The temporary UCI transaction could not be created.'),
	uci_validation_failed: _('The generated network configuration is invalid.'),
	settings_commit_failed: _('The application settings could not be committed.'),
	state_directory_failed: _('The protected registration directory could not be created.'),
	state_write_failed: _('The protected registration data could not be saved.'),
	lock_failed: _('The operation lock could not be acquired.'),
	manager_unavailable: _('The WARP manager is unavailable.'),
	terms_not_accepted: _('Cloudflare terms were not accepted.'),
	invalid_action: _('The requested operation is not allowed.')
};

function resultMessage(result) {
	if (result && result.code && errorLabels[result.code])
		return errorLabels[result.code];
	return _('The WARP operation failed.');
}

return view.extend({
	load: function() {
		return Promise.all([
			L.resolveDefault(callStatus(), { ok: false, state: 'error', error_code: 'manager_unavailable' }),
			uci.load('warp')
		]);
	},

	runAction: function(title, promise) {
		ui.showModal(title, [
			E('p', { 'class': 'spinning' }, _('Please wait…'))
		]);
		return promise.then(function(result) {
			ui.hideModal();
			if (!result || result.ok !== true) {
				ui.addNotification(null, E('p', resultMessage(result)), 'error');
				return;
			}
			ui.addNotification(null, E('p', _('Operation completed successfully.')), 'info');
			window.setTimeout(function() { window.location.reload(); }, 350);
		}).catch(function(err) {
			ui.hideModal();
			ui.addNotification(null, E('p', _('RPC request failed: %s').format(err.message)), 'error');
		});
	},

	confirmRegistration: function() {
		const self = this;
		ui.showModal(_('Connect WARP'), [
			E('p', {}, _('Registration connects this router to the third-party Cloudflare service and sends a generated public key and device metadata. No private key is sent.')),
			E('p', {}, [
				E('a', {
					'href': 'https://www.cloudflare.com/application/terms/',
					'target': '_blank',
					'rel': 'noreferrer noopener'
				}, _('Cloudflare terms of use')),
				' · ',
				E('a', {
					'href': 'https://www.cloudflare.com/privacypolicy/',
					'target': '_blank',
					'rel': 'noreferrer noopener'
				}, _('Cloudflare privacy policy'))
			]),
			E('div', { 'class': 'right' }, [
				E('button', {
					'class': 'btn',
					'click': ui.hideModal
				}, _('Cancel')),
				' ',
				E('button', {
					'class': 'btn cbi-button-action important',
					'click': function() {
						ui.hideModal();
						return self.runAction(_('Registering WARP'), callRegister(true));
					}
				}, _('Accept and connect'))
			])
		]);
	},

	confirmUnregister: function() {
		const self = this;
		ui.showModal(_('Remove registration'), [
			E('p', {}, _('The managed interface and local registration secrets will be removed. Other network and firewall settings are not touched.')),
			E('div', { 'class': 'right' }, [
				E('button', { 'class': 'btn', 'click': ui.hideModal }, _('Cancel')),
				' ',
				E('button', {
					'class': 'btn cbi-button-negative important',
					'click': function() {
						ui.hideModal();
						return self.runAction(_('Removing registration'), callUnregister());
					}
				}, _('Remove registration'))
			])
		]);
	},

	renderStatus: function(status) {
		const state = stateLabels[status.state] || _('Unknown');
		const rows = [
			_('Status'), state,
			_('Interface'), status.interface || _('Not created')
		];
		if (status.backend)
			rows.push(_('Backend'), backendLabels[status.backend] || status.backend);
		if (status.endpoint)
			rows.push(_('Endpoint'), status.endpoint);
		if (status.error_code)
			rows.push(_('Last error'), errorLabels[status.error_code] || status.error_code);

		const table = E('table', { 'class': 'table' });
		for (let i = 0; i < rows.length; i += 2)
			table.appendChild(E('tr', { 'class': 'tr' }, [
				E('td', { 'class': 'td left', 'width': '33%' }, rows[i]),
				E('td', { 'class': 'td left' }, rows[i + 1])
			]));
		return table;
	},

	renderActions: function(status) {
		const self = this;
		const buttons = [];
		if (!status.registered) {
			buttons.push(E('button', {
				'class': 'btn cbi-button-action important',
				'click': function() { return self.confirmRegistration(); }
			}, _('Connect WARP')));
		}
		else {
			if (status.state === 'disabled')
				buttons.push(E('button', {
					'class': 'btn cbi-button-action important',
					'click': function() { return self.runAction(_('Enabling WARP'), callEnable()); }
				}, _('Connect WARP')));
			else
				buttons.push(E('button', {
					'class': 'btn cbi-button-neutral',
					'click': function() { return self.runAction(_('Disabling WARP'), callDisable()); }
				}, _('Disconnect')));
			buttons.push(' ', E('button', {
				'class': 'btn cbi-button-action',
				'click': function() { return self.runAction(_('Reconnecting WARP'), callReconnect()); }
			}, _('Reconnect')));
			buttons.push(' ', E('button', {
				'class': 'btn cbi-button-negative',
				'click': function() { return self.confirmUnregister(); }
			}, _('Remove registration')));
		}
		return E('div', { 'class': 'cbi-section-actions' }, buttons);
	},

	render: function(data) {
		const status = data[0] || {};
		const map = new form.Map('warp', _('Settings'), _('Creates a separate WARP interface without changing routes, DNS or firewall.'));
		const section = map.section(form.NamedSection, 'main', 'warp');

		let option = section.option(form.Flag, 'auto_start', _('Start automatically'));
		option.default = option.enabled;
		option.rmempty = false;

		option = section.option(form.Value, 'interface', _('Preferred interface name'));
		option.default = 'warp';
		option.rmempty = false;
		option.validate = function(sectionId, value) {
			return /^[A-Za-z_][A-Za-z0-9_]{0,14}$/.test(value) ? true : _('Use 1–15 letters, digits or underscores; the first character must not be a digit.');
		};

		option = section.option(form.Value, 'mtu', _('MTU'));
		option.datatype = 'range(576,1420)';
		option.default = '1280';
		option.rmempty = false;

		option = section.option(form.Value, 'keepalive', _('Persistent keepalive'));
		option.datatype = 'range(0,65535)';
		option.default = '25';
		option.rmempty = false;

		option = section.option(form.Value, 'sni', _('Masking hostname'));
		option.placeholder = 'ozon.ru';
		option.default = 'ozon.ru';
		option.rmempty = false;
		option.description = _('Used only in the fake first AWG packet; it does not change DNS.');
		option.validate = function(sectionId, value) {
			if (value.length > 253 || !/^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)*[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/.test(value))
				return _('Enter a valid DNS hostname without a scheme or port.');
			return true;
		};

		option = section.option(form.Value, 'exclude_countries', _('Exclude exit countries'));
		option.placeholder = 'RU';
		option.default = 'RU';
		option.optional = true;
		option.description = _('Comma-separated ISO country codes. The default avoids Russian WARP exits.');
		option.validate = function(sectionId, value) {
			return !value || /^[A-Za-z]{2}(,[A-Za-z]{2})*$/.test(value) ? true : _('Use comma-separated two-letter country codes, for example RU,BY.');
		};

		return map.render().then(L.bind(function(formNode) {
			const content = [
				E('h2', {}, _('Cloudflare WARP')),
				E('div', { 'class': 'cbi-section' }, [
					E('p', {}, _('Traffic uses WAN until you select this interface in your routing configuration. Reconnect scans for a new verified endpoint.')),
					this.renderStatus(status),
					this.renderActions(status)
				])
			];
			if (status.interface && status.managed)
				content.push(E('p', {}, E('a', {
					'class': 'btn cbi-button-link',
					'href': L.url('admin/network/network')
				}, _('Open interface %s').format(status.interface))));
			content.push(formNode);
			return E('div', {}, content);
		}, this));
	}
});
