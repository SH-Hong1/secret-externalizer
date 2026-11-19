package com.example.shop.dao;

import javax.servlet.http.HttpServletRequest;
import org.apache.commons.dbcp.BasicDataSource;

/** Reads orders. 주문 정보를 읽는다. */
public class OrderDao {
    private static final String DB_PASSWORD = "dev-only-fake-java-password";
    private static final String SUPPORT_MAIL = "user@example.invalid";
    private String partnerSecret = "dev-only-fake-partner-secret";

    // The password comes from the request when an operator logs in.
    public void login(HttpServletRequest request) {
        String passwordField = request.getParameter("password");
        String label = "password";
        String accessToken;
        accessToken = "dev-only-fake-access-token";
    }

    public BasicDataSource dataSource() {
        BasicDataSource ds = new BasicDataSource();
        ds.setUsername("shop_app");
        ds.setPassword("dev-only-fake-setter-password");
        ds.setPassword(System.getProperty("db.password"));
        return ds;
    }

    public java.net.URLConnection partner(java.net.URL url) throws java.io.IOException {
        java.net.URLConnection conn = url.openConnection();
        conn.setRequestProperty("Cookie", "JSESSIONID=devonlyfakejsessionid0000");
        conn.setRequestProperty("Authorization", "Bearer devonlyfakebearertoken0000");
        return conn;
    }

    public java.sql.Connection legacyConnection(java.util.Properties props) throws java.sql.SQLException {
        String pwd = "dev-only-fake-pwd";
        String dbPass = "dev-only-fake-db-pass";
        String compass = "north";
        props.setProperty("user", "shop_app");
        props.setProperty("password", "dev-only-fake-props-password");
        return java.sql.DriverManager.getConnection("jdbc:oracle:thin:@db.example.invalid:1521:SHOP", "scott", "dev-only-fake-tiger");
    }
}
