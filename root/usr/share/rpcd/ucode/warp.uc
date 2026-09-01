#!/usr/bin/ucode

'use strict';

import { popen } from 'fs';

const manager = '/usr/libexec/warp-manager';

function run(action) {
	const allowed = {
		status: true,
		register: true,
		enable: true,
		disable: true,
		reconnect: true,
		unregister: true,
	};

	if (allowed[action] !== true)
		return { ok: false, code: 'invalid_action' };

	const fd = popen(manager + ' ' + action, 'r');
	if (!fd)
		return { ok: false, code: 'manager_unavailable' };

	const output = fd.read('all');
	fd.close();

	try {
		const result = json(trim(output));
		if (type(result) == 'object' && type(result.ok) == 'bool')
			return result;
	}
	catch (e) {}

	return { ok: false, code: 'invalid_manager_response' };
}

const methods = {
	status: {
		call: function() {
			return run('status');
		}
	},
	register: {
		args: { accept_terms: false },
		call: function(request) {
			if (request.args.accept_terms !== true)
				return { ok: false, code: 'terms_not_accepted' };
			return run('register');
		}
	},
	enable: {
		call: function() {
			return run('enable');
		}
	},
	disable: {
		call: function() {
			return run('disable');
		}
	},
	reconnect: {
		call: function() {
			return run('reconnect');
		}
	},
	unregister: {
		call: function() {
			return run('unregister');
		}
	}
};

return { 'luci.warp': methods };
