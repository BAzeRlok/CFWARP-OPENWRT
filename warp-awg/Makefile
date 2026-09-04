include $(TOPDIR)/rules.mk

PKG_NAME:=warp-awg
PKG_VERSION:=3.1.20260828
PKG_RELEASE:=3

PKG_SOURCE:=amneziawg-go-$(PKG_VERSION).tar.gz
PKG_SOURCE_URL:=https://codeload.github.com/amnezia-vpn/amneziawg-go/tar.gz/refs/tags/v$(PKG_VERSION)?
PKG_HASH:=24c656cfb80ff6855702710eaf2e3729fa710bf6bfdbbbdfba01984ccd17de95
PKG_BUILD_DIR:=$(BUILD_DIR)/amneziawg-go-$(PKG_VERSION)

PKG_LICENSE:=MIT
PKG_LICENSE_FILES:=LICENSE
PKG_MAINTAINER:=luci-app-warp contributors

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=github.com/amnezia-vpn/amneziawg-go/v3
GO_PKG_BUILD_PKG:=$(GO_PKG)

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/warp-awg
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Userspace AmneziaWG backend for luci-app-warp
  URL:=https://github.com/amnezia-vpn/amneziawg-go
  DEPENDS:=$(GO_ARCH_DEPENDS) +kmod-tun
endef

define Package/warp-awg/description
  A private userspace AmneziaWG daemon and minimal UAPI controller used by
  luci-app-warp. It does not require a kernel-specific AmneziaWG module.
endef

define Build/Prepare
	$(call Build/Prepare/Default)
	$(SED) 's/^const Version = .*/const Version = "$(PKG_VERSION)"/' $(PKG_BUILD_DIR)/version.go
endef

define Build/Compile
	$(call GoPackage/Build/Compile)
	$(TARGET_CC) $(TARGET_CPPFLAGS) $(TARGET_CFLAGS) $(TARGET_LDFLAGS) \
		-std=c11 -Wall -Wextra -Werror -o $(PKG_BUILD_DIR)/warp-awgctl \
		$(CURDIR)/files/warp-awgctl.c
endef

define Package/warp-awg/install
	$(INSTALL_DIR) $(1)/usr/libexec
	$(INSTALL_BIN) $(GO_PKG_BUILD_BIN_DIR)/amneziawg-go $(1)/usr/libexec/warp-amneziawg-go
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/warp-awgctl $(1)/usr/libexec/warp-awgctl
endef

$(eval $(call BuildPackage,warp-awg))
