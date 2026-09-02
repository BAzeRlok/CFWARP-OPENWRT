include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-warp
PKG_VERSION:=1.7.1
PKG_RELEASE:=1
PKG_LICENSE:=Apache-2.0
PKG_MAINTAINER:=luci-app-warp contributors

LUCI_TITLE:=LuCI support for a standalone Cloudflare WARP interface
LUCI_DESCRIPTION:=Register Cloudflare WARP and create a route-free standalone MASQUE tunnel interface
LUCI_DEPENDS:=+luci-base +rpcd +rpcd-mod-ucode +curl +ca-bundle +jsonfilter
LUCI_EXTRA_DEPENDS:=kmod-tun (>=0), warp-usque (>=4.2.1-r4)
LUCI_PKGARCH:=all
# Keep the conventional LuCI package identity even when this source tree is
# checked out under a differently named directory.
LUCI_NAME:=luci-app-warp

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
