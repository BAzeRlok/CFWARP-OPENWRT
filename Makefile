include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-warp
PKG_VERSION:=2.0.1
PKG_RELEASE:=1
PKG_LICENSE:=Apache-2.0
PKG_MAINTAINER:=luci-app-warp contributors

LUCI_TITLE:=LuCI support for a standalone Cloudflare WARP interface
LUCI_DESCRIPTION:=Register or remove Cloudflare WARP profiles and manage a route-free standalone MASQUE over QUIC interface
LUCI_DEPENDS:=+luci-base +rpcd +rpcd-mod-ucode +curl +ca-bundle +jsonfilter
LUCI_EXTRA_DEPENDS:=kmod-tun (>=0), warp-usque (>=4.2.1-r9)
LUCI_PKGARCH:=all
# Keep the conventional LuCI package identity even when this source tree is
# checked out under a differently named directory.
LUCI_NAME:=luci-app-warp

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
