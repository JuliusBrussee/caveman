# CORS Preflight Debugging

## Problem

A React frontend at `https://app.example.com` makes a POST request with JSON to an API at `https://api.example.com`. The browser console shows: "Access to XMLHttpRequest has been blocked by CORS policy: Response to preflight request doesn't pass access control check."

The API has `Access-Control-Allow-Origin: *` set. Why is it still failing and how do you fix it?
